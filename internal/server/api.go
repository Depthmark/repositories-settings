package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/applier"
	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

type reconcileHandler struct {
	deps Deps
}

type reconcileRequest struct {
	Owner        string   `json:"owner"`
	Repo         string   `json:"repo"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	DryRun       bool     `json:"dry_run,omitempty"`
}

func (h *reconcileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !authorize(r, h.deps) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req reconcileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Owner == "" || req.Repo == "" {
		http.Error(w, "owner and repo required", http.StatusBadRequest)
		return
	}
	repo := config.Repo{Owner: req.Owner, Name: req.Repo}
	settings, err := h.deps.Settings(r.Context(), repo, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.ChangedFiles != nil {
		if keys := config.AffectedKeys(req.ChangedFiles); keys != nil {
			settings = config.Filter(settings, keys)
		}
	}
	rep, err := h.deps.Reconciler.Reconcile(r.Context(), h.deps.Client, repo, settings, config.TriggerManual, req.DryRun, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

type checkHandler struct {
	deps Deps
}

type checkRequest struct {
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	HeadSHA  string `json:"head_sha,omitempty"`
	PRNumber *int   `json:"pr_number,omitempty"`
}

type checkResponse struct {
	Conclusion string             `json:"conclusion"`
	Summary    string             `json:"summary"`
	Report     *reconciler.Report `json:"report"`
}

// ServeHTTP runs a dry-run reconcile against the PR's head SHA and
// returns a conclusion the PR Check workflow can branch on.
//
// Conclusions:
//   - "success" — settings already match (no actionable diffs, no lane errors).
//   - "neutral" — changes would apply on merge but nothing prevents that.
//   - "failure" — at least one lane errored during the dry-run (e.g. invalid
//     config, missing permissions). The workflow exits non-zero on this.
func (h *checkHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !authorize(r, h.deps) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Owner == "" || req.Repo == "" {
		http.Error(w, "owner and repo required", http.StatusBadRequest)
		return
	}
	repo := config.Repo{Owner: req.Owner, Name: req.Repo}
	settings, err := h.deps.Settings(r.Context(), repo, req.HeadSHA)
	if err != nil {
		metrics.PRCheckTotal.WithLabelValues("failure").Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep, err := h.deps.Reconciler.Reconcile(r.Context(), h.deps.Client, repo, settings, config.TriggerWebhook, true, nil)
	if err != nil {
		metrics.PRCheckTotal.WithLabelValues("failure").Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	conclusion := "success"
	switch {
	case anyLaneFailed(rep.Applied):
		conclusion = "failure"
	case anyActionableDiff(rep.Diffs):
		conclusion = "neutral"
	}
	metrics.PRCheckTotal.WithLabelValues(conclusion).Inc()
	writeJSON(w, http.StatusOK, checkResponse{
		Conclusion: conclusion,
		Summary:    diff.FormatMarkdown(rep.Diffs),
		Report:     rep,
	})
}

func anyLaneFailed(rs []applier.Result) bool {
	for _, r := range rs {
		if !r.Success {
			return true
		}
	}
	return false
}

func anyActionableDiff(ds []diff.Diff) bool {
	for _, d := range ds {
		if d.Action != diff.Noop {
			return true
		}
	}
	return false
}

type validateHandler struct {
	deps Deps
}

type validateRequest struct {
	Files []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"files"`
}

type validateResponse struct {
	Valid bool                  `json:"valid"`
	Files []validateFileOutcome `json:"files"`
}
type validateFileOutcome struct {
	File   string   `json:"file"`
	Valid  bool     `json:"valid"`
	Issues []string `json:"issues,omitempty"`
}

func (h *validateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	out := validateResponse{Valid: true}
	files := map[string][]byte{}
	for _, f := range req.Files {
		files[f.Name] = []byte(f.Content)
	}
	// LoadFromMap reports per-file errors via errors.Join.
	if _, err := config.LoadFromMap(files); err != nil {
		out.Valid = false
		// Try to attribute each issue to its file by inspecting
		// errors.Join wrappers (each leaf is a *config.ValidationError).
		var ve *config.ValidationError
		var joined interface{ Unwrap() []error }
		if errors.As(err, &joined) {
			for _, leaf := range joined.Unwrap() {
				if errors.As(leaf, &ve) {
					out.Files = append(out.Files, validateFileOutcome{
						File: ve.File, Valid: false, Issues: ve.Issues,
					})
				} else {
					out.Files = append(out.Files, validateFileOutcome{
						Valid: false, Issues: []string{leaf.Error()},
					})
				}
			}
		} else if errors.As(err, &ve) {
			out.Files = append(out.Files, validateFileOutcome{
				File: ve.File, Valid: false, Issues: ve.Issues,
			})
		} else {
			out.Files = append(out.Files, validateFileOutcome{
				Valid: false, Issues: []string{err.Error()},
			})
		}
	}
	// Mark unmentioned files as valid.
	mentioned := map[string]bool{}
	for _, f := range out.Files {
		mentioned[f.File] = true
	}
	for _, f := range req.Files {
		if !mentioned[f.Name] {
			out.Files = append(out.Files, validateFileOutcome{File: f.Name, Valid: true})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func authorize(r *http.Request, d Deps) bool {
	if d.APIToken == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	return auth[len(prefix):] == d.APIToken
}

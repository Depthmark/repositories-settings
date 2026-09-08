package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/applier"
	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

// reuse the formatter shared with the bot's sticky PR comment so the
// /api/check `summary` looks identical to what the bot posts.
var formatReport = reconciler.FormatReportMarkdown

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
	c, ok := requireCaller(w, r, h.deps)
	if !ok {
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
	// An OIDC caller may only reconcile the repository whose workflow
	// minted its token, so the scope check waits for the target.
	if !requireScope(w, r, h.deps, c, repo) {
		return
	}
	if !req.DryRun && !requireApply(w, r, h.deps, c, repo) {
		return
	}
	res, err := h.deps.Settings(r.Context(), repo, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.ChangedFiles != nil {
		res = res.Filtering(config.AffectedKeys(req.ChangedFiles))
	}
	rep, err := reconcile(r.Context(), h.deps, repo, res, config.TriggerManual, req.DryRun)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Populate the rendered summary so workflow consumers can write it
	// straight to $GITHUB_STEP_SUMMARY without re-implementing the
	// formatter. The same string powers /api/check's top-level summary
	// field; here we hang it off the Report.
	rep.Summary = formatReport(rep)
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
	c, ok := requireCaller(w, r, h.deps)
	if !ok {
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
	if !requireScope(w, r, h.deps, c, repo) {
		return
	}
	res, err := h.deps.Settings(r.Context(), repo, req.HeadSHA)
	if err != nil {
		metrics.PRCheckTotal.WithLabelValues("failure").Inc()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep, err := reconcile(r.Context(), h.deps, repo, res, config.TriggerWebhook, true)
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
		Summary:    formatReport(rep),
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
	// Disabled lists the operator-disabled resource keys touched by
	// any of the submitted files. Top-level rollup so callers can
	// branch on "are any disabled sections in play?" without scanning
	// per-file outcomes. Empty when the operator hasn't disabled
	// anything or none of the files reference a disabled resource.
	Disabled []string `json:"disabled,omitempty"`
}
type validateFileOutcome struct {
	File   string   `json:"file"`
	Valid  bool     `json:"valid"`
	Issues []string `json:"issues,omitempty"`
	// Disabled lists the canonical resource keys this file configures
	// that the operator has disabled. The file itself is structurally
	// valid (Valid=true); the warning is "your YAML is fine but the
	// service will ignore this section."
	Disabled []string `json:"disabled,omitempty"`
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

	// A validation endpoint that answers 200 for invalid input makes
	// every caller parse the body to find out. 422 says "well-formed
	// request, unprocessable content", which is what a schema failure
	// is, and lets a workflow branch on the status line.
	status := http.StatusOK
	if !out.Valid {
		status = http.StatusUnprocessableEntity
	}

	// Annotate each file with any operator-disabled resources it
	// configures. We do this after structural validation so a syntactically
	// broken file still gets its `issues`, plus the disabled warning if
	// the file's identity is known.
	if len(h.deps.DisabledResources) > 0 {
		seenTopLevel := map[string]bool{}
		for i, f := range out.Files {
			for _, key := range config.ResourceKeysForFile(f.File) {
				if h.deps.DisabledResources.Has(key) {
					out.Files[i].Disabled = append(out.Files[i].Disabled, key)
					if !seenTopLevel[key] {
						seenTopLevel[key] = true
						out.Disabled = append(out.Disabled, key)
					}
				}
			}
		}
	}
	writeJSON(w, status, out)
}

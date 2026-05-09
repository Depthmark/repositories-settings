package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

type webhookHandler struct {
	deps Deps
}

// genericPayload is the union of fields we read from the various event
// shapes. Keeping it flat lets us decode once and dispatch on event +
// action without a per-event struct.
type genericPayload struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Merged bool `json:"merged"`
	} `json:"pull_request"`
	Issue struct {
		Number      int `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request,omitempty"`
	} `json:"issue"`
	Comment struct {
		Body              string `json:"body"`
		AuthorAssociation string `json:"author_association"`
		User              struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	Ref string `json:"ref"`
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	if len(h.deps.WebhookSecret) > 0 {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifyHMAC(h.deps.WebhookSecret, body, sig) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	event := r.Header.Get("X-GitHub-Event")
	var p genericPayload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	metrics.WebhookEventsTotal.WithLabelValues(event, p.Action).Inc()

	repo := config.Repo{Owner: p.Repository.Owner.Login, Name: p.Repository.Name}
	if repo.Owner == "" || repo.Name == "" {
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "no repo"})
		return
	}

	switch event {
	case "push":
		h.handlePush(w, r.Context(), repo, p)
	case "pull_request":
		h.handlePullRequest(w, r.Context(), repo, p)
	case "issue_comment":
		h.handleIssueComment(w, r.Context(), repo, p)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "unhandled event"})
	}
}

// handlePush reconciles on default-branch pushes (we don't filter by
// branch here — the reconciler is idempotent and a push from a feature
// branch still carries valuable signal for selective reconcile).
func (h *webhookHandler) handlePush(w http.ResponseWriter, ctx context.Context, repo config.Repo, p genericPayload) {
	ref := ""
	if strings.HasPrefix(p.Ref, "refs/heads/") {
		ref = strings.TrimPrefix(p.Ref, "refs/heads/")
	}
	settings, err := h.deps.Settings(ctx, repo, ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep, err := h.deps.Reconciler.Reconcile(ctx, h.deps.Client, repo, settings, config.TriggerPush, false, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handlePullRequest runs a dry-run on opened/synchronize/reopened and
// posts (or updates) a sticky comment showing the diff or any config
// errors. On merged-close it runs a real reconcile.
func (h *webhookHandler) handlePullRequest(w http.ResponseWriter, ctx context.Context, repo config.Repo, p genericPayload) {
	switch p.Action {
	case "opened", "synchronize", "reopened":
		h.runDryRunAndComment(w, ctx, repo, p.PullRequest.Number, p.PullRequest.Head.SHA)
	case "closed":
		if !p.PullRequest.Merged {
			writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "pr not merged"})
			return
		}
		settings, err := h.deps.Settings(ctx, repo, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rep, err := h.deps.Reconciler.Reconcile(ctx, h.deps.Client, repo, settings, config.TriggerWebhook, false, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rep)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "unhandled action"})
	}
}

// handleIssueComment dispatches @-mentions of the bot. Only "created"
// actions on PR-attached issues are interesting; edits and reactions
// would multiply event noise.
func (h *webhookHandler) handleIssueComment(w http.ResponseWriter, ctx context.Context, repo config.Repo, p genericPayload) {
	if p.Action != "created" {
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "non-create action"})
		return
	}
	if p.Issue.PullRequest == nil {
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "not a PR"})
		return
	}
	// Suppress self-replies: a Bot user posting to its own PR comment
	// could otherwise infinite-loop us.
	if strings.EqualFold(p.Comment.User.Type, "Bot") {
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "bot author"})
		return
	}

	cmd := ParseComment(p.Comment.Body, h.deps.AppSlug)
	if cmd.Command == CmdNone {
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "no command"})
		return
	}

	switch cmd.Command {
	case CmdHelp:
		_, _ = upsertStickyComment(ctx, h.deps.Client, repo, p.Issue.Number, HelpMarkdown(h.deps.AppSlug))
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "command": "help"})
	case CmdRecheck:
		// Reuse the dry-run flow against the PR head. We don't have the
		// SHA on issue_comment, so the loader reads the latest from the
		// PR's branch via the API in Settings(); passing "" defers to it.
		h.runDryRunAndComment(w, ctx, repo, p.Issue.Number, "")
	case CmdApply:
		if !IsPrivilegedAuthor(p.Comment.AuthorAssociation) {
			body := fmt.Sprintf(":lock: `apply` requires write access; @%s has association `%s`.",
				p.Comment.User.Login, strings.ToLower(p.Comment.AuthorAssociation))
			_, _ = upsertStickyComment(ctx, h.deps.Client, repo, p.Issue.Number, body)
			writeJSON(w, http.StatusOK, map[string]any{"received": true, "rejected": "unprivileged"})
			return
		}
		settings, err := h.deps.Settings(ctx, repo, "")
		if err != nil {
			h.commentError(ctx, repo, p.Issue.Number, "loading config", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rep, err := h.deps.Reconciler.Reconcile(ctx, h.deps.Client, repo, settings, config.TriggerManual, false, nil)
		if err != nil {
			h.commentError(ctx, repo, p.Issue.Number, "reconcile", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = upsertStickyComment(ctx, h.deps.Client, repo, p.Issue.Number, formatApplyReport(rep))
		writeJSON(w, http.StatusOK, rep)
	}
}

// runDryRunAndComment loads settings at headSHA, runs a dry-run, and
// updates the sticky comment with either the diff or the validation
// errors. It writes the report to the HTTP response so the caller sees
// what we did.
func (h *webhookHandler) runDryRunAndComment(
	w http.ResponseWriter,
	ctx context.Context,
	repo config.Repo,
	prNumber int,
	headSHA string,
) {
	settings, err := h.deps.Settings(ctx, repo, headSHA)
	if err != nil {
		// Configuration errors from the loader are the operator's
		// problem to fix, so render them in the PR. Failures from
		// network/auth are not — they go to the HTTP response only.
		if isConfigError(err) {
			_, _ = upsertStickyComment(ctx, h.deps.Client, repo, prNumber, formatConfigErrors(err))
			writeJSON(w, http.StatusOK, map[string]any{"received": true, "config_error": err.Error()})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep, err := h.deps.Reconciler.Reconcile(ctx, h.deps.Client, repo, settings, config.TriggerWebhook, true, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body := formatDryRunComment(rep)
	if _, err := upsertStickyComment(ctx, h.deps.Client, repo, prNumber, body); err != nil {
		// Comment failure shouldn't fail the webhook ack — log and
		// return the report.
		h.deps.Logger.Warn("sticky comment failed", "err", err, "repo", repo.String(), "pr", prNumber)
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *webhookHandler) commentError(ctx context.Context, repo config.Repo, prNumber int, phase string, err error) {
	body := fmt.Sprintf(":warning: %s failed: `%s`", phase, err.Error())
	_, _ = upsertStickyComment(ctx, h.deps.Client, repo, prNumber, body)
}

// formatDryRunComment is the body of the sticky comment we post on
// PR opens / synchronize. The formatter owns the entire body —
// including the verdict line and the apply hint — so the bot's
// sticky comment and the workflow's /api/check `summary` are
// byte-identical. No wrapper here.
func formatDryRunComment(rep *reconciler.Report) string {
	return reconciler.FormatReportMarkdown(rep)
}

// formatApplyReport is the body posted after a successful apply.
func formatApplyReport(rep *reconciler.Report) string {
	var b strings.Builder
	b.WriteString("## repo-settings — applied\n\n")
	if len(rep.Applied) == 0 {
		b.WriteString("No mutations were necessary; configuration already matched.\n")
		return b.String()
	}
	ok, fail := 0, 0
	for _, r := range rep.Applied {
		if r.Success {
			ok++
		} else {
			fail++
		}
	}
	fmt.Fprintf(&b, "- :white_check_mark: %d successful\n", ok)
	if fail > 0 {
		fmt.Fprintf(&b, "- :x: %d failed\n\n", fail)
		for _, r := range rep.Applied {
			if r.Success {
				continue
			}
			fmt.Fprintf(&b, "  - `%s`: %s\n", r.Resource, r.Error)
		}
	}
	return b.String()
}

// formatConfigErrors renders a friendly view of validation errors so
// the contributor sees what to fix. errors.Join leaves are unwrapped.
func formatConfigErrors(err error) string {
	var b strings.Builder
	b.WriteString("## repo-settings — configuration error\n\n")
	b.WriteString("The bot couldn't parse the configuration in this branch:\n\n")
	for _, leaf := range flattenErrors(err) {
		var ve *config.ValidationError
		if errors.As(leaf, &ve) {
			fmt.Fprintf(&b, "**`%s`**\n", ve.File)
			for _, issue := range ve.Issues {
				fmt.Fprintf(&b, "- %s\n", issue)
			}
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "- %s\n", leaf.Error())
	}
	b.WriteString("\nFix the file(s) above and push again, or comment `recheck` to retry.")
	return b.String()
}

// flattenErrors turns an errors.Join tree into a flat slice of leaves.
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}
	type joined interface{ Unwrap() []error }
	if j, ok := err.(joined); ok {
		var out []error
		for _, child := range j.Unwrap() {
			out = append(out, flattenErrors(child)...)
		}
		return out
	}
	return []error{err}
}

// isConfigError reports whether err originated in the YAML loader and
// is therefore actionable from the PR.
func isConfigError(err error) bool {
	var ve *config.ValidationError
	if errors.As(err, &ve) {
		return true
	}
	for _, leaf := range flattenErrors(err) {
		if errors.As(leaf, &ve) {
			return true
		}
	}
	return false
}

func verifyHMAC(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(header[len(prefix):]))
}

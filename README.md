# repositories-settings

Declarative GitHub repository configuration-as-code. Reconciles per-repo
YAML configs in `.github/settings/` against live GitHub state and applies
the diff. Go port of [`repository-settings`](https://github.com/Depthmark/repository-settings)
(TypeScript / Probot).

## What it does

A GitHub App that owns repository settings declaratively. You write
`.github/settings/*.yml` files in each managed repo (or org-wide
defaults); the app keeps GitHub in sync with those files.

Triggers:

- **Webhooks** — push and merged-PR events drive a reconcile; PR `opened`/
  `synchronize` runs a dry-run and updates a sticky PR comment with the
  diff (or with validation errors when the YAML can't be parsed).
- **PR comment commands** — mention the bot (`@repo-settings recheck`,
  `@repo-settings apply`) to refresh the diff or apply the configuration
  without leaving the PR. See [PR commands](#pr-commands) below.
- **HTTP API** — `POST /api/reconcile` for ad-hoc or selective runs;
  `POST /api/validate` to check a YAML file before committing;
  `POST /api/check` for the workflow-driven dry-run path.
- **Cron worker** — periodic batch reconcile across many repos with
  GraphQL prefetch.

## Architecture

```
Trigger (webhook / API / cron)
        │
        ▼
 Reconciler.Reconcile          per-repo, one unit of work
        │
        ▼
 Phase A — repo + topics        (sequential; archival/visibility gate
        │                        downstream)
        ▼
 Phase B — 13 lanes in parallel via errgroup
   teams · rulesets · environments · webhooks · autolinks ·
   actions · security · pages · secrets · variables · deploy_keys ·
   custom_properties · collaborators · branches
        │
        └─ each lane: GetLive → Plan → Apply (bounded fan-out, n=5)
```

### Key invariants

- **Per-lane error isolation.** A lane failure produces a single
  synthetic `ApplyResult` with `Success: false`; the rest of the
  reconcile completes.
- **One-sided diffs.** Fields present in live GitHub state but absent in
  desired config are *not* clobbered — that's how unmanaged settings
  remain unmanaged.
- **Bounded per-applier concurrency.** A 100-item diff doesn't fire 100
  simultaneous requests; capped at 5.
- **Two-pool rate limiter.** Separate REST/GraphQL budgets, priority
  queue, exponential backoff with 25% jitter when both pools are
  exhausted.
- **Conditional GETs.** Stable read endpoints flow through an
  `http.RoundTripper` that adds `If-None-Match` and substitutes a cached
  body on `304 Not Modified`. Writes invalidate by URL prefix.

## Layout

```
cmd/repo-settings/        entry point (DI wiring, signal handling)
internal/
  config/                 schema, validation, loader, suborg/admin policy,
                          selective-reconcile mapping
  diff/                   pure-logic deep-compare + named-list / single-
                          resource diff + markdown formatting
  conc/                   bounded fan-out helper
  ghclient/               App auth, two-pool rate limiter, conditional-GET
                          transport, Link-header pagination, GraphQL helpers
  applier/                14 per-resource appliers (one Lane closure each)
  reconciler/             phased orchestrator
  worker/                 cron batch with GraphQL prefetch
  server/                 HTTP server (webhook, /api/*, /healthz, /readyz,
                          /metrics) + middleware
  metrics/                Prometheus registry
  logger/                 slog construction + helpers
plan.md                   port plan + redesign notes (read this first)
```

## Configuration

Environment variables (all optional; sensible defaults):

| Var                              | Default                  | Purpose                                                    |
| -------------------------------- | ------------------------ | ---------------------------------------------------------- |
| `ADDR`                           | `:8080`                  | HTTP listen address                                        |
| `LOG_LEVEL`                      | `info`                   | `debug` / `info` / `warn` / `error`                        |
| `GITHUB_API_URL`                 | `https://api.github.com` | GitHub Enterprise users override                           |
| `GITHUB_APP_ID`                  | (none)                   | GitHub App ID                                              |
| `GITHUB_APP_PRIVATE_KEY_PATH`    | (none)                   | Path to PEM file for the App                               |
| `GITHUB_WEBHOOK_SECRET`          | (none)                   | HMAC secret; if set, `/webhook` rejects unsigned payloads  |
| `API_TOKEN`                      | (none)                   | If set, `/api/reconcile` requires `Authorization: Bearer …`|
| `APP_SLUG`                       | `repo-settings`          | The bot's @-name; used to recognise PR-comment commands    |
| `DISABLED_RESOURCES`             | (none)                   | CSV of resource keys the deployment refuses to manage (see [Disabling resources](#disabling-resources)) |

## Endpoints

| Path                  | Auth                                | Purpose                                       |
| --------------------- | ----------------------------------- | --------------------------------------------- |
| `POST /webhook`       | `X-Hub-Signature-256` (HMAC SHA256) | GitHub event intake                           |
| `POST /api/reconcile` | `Authorization: Bearer <API_TOKEN>` | Manual reconcile (`{owner, repo, dry_run, changed_files}`) |
| `POST /api/validate`  | none                                | Validate YAML files (`{files: [{name, content}]}`)         |
| `GET /healthz`        | none                                | Liveness                                      |
| `GET /readyz`         | none                                | Readiness (returns 503 during shutdown)       |
| `GET /metrics`        | none                                | Prometheus metrics                            |

## GitHub App manifest

This service runs as a GitHub App. Create the App by `POST`ing the
manifest below to `https://github.com/organizations/<ORG>/settings/apps/new?state=<state>`
(or the user-account equivalent) — the [App-from-manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest)
auto-generates the App ID, private key, and webhook secret.

Replace `https://repo-settings.example.com` with your deployment URL.

```json
{
  "name": "repositories-settings",
  "url": "https://github.com/Depthmark/repositories-settings",
  "description": "Declarative GitHub repository configuration-as-code. Reconciles .github/settings/*.yml against live repo state.",
  "public": false,
  "redirect_url": "https://repo-settings.example.com/api/manifest/callback",
  "hook_attributes": {
    "url": "https://repo-settings.example.com/webhook",
    "active": true
  },
  "default_events": [
    "push",
    "pull_request",
    "issue_comment",
    "repository"
  ],
  "default_permissions": {
    "administration": "write",
    "actions": "write",
    "checks": "write",
    "contents": "read",
    "deployments": "write",
    "environments": "write",
    "issues": "read",
    "metadata": "read",
    "pages": "write",
    "pull_requests": "write",
    "secrets": "write",
    "actions_variables": "write",
    "repository_hooks": "write",
    "repository_custom_properties": "write",
    "members": "write",
    "organization_administration": "read",
    "organization_custom_properties": "admin"
  }
}
```

### Why each permission is required

| Permission                          | Level | Used for                                                                                                |
| ----------------------------------- | ----- | ------------------------------------------------------------------------------------------------------- |
| `administration: write`             | repo  | `PATCH /repos/:o/:r` (settings), topics, autolinks, branch protection, deploy keys, rulesets, collaborator add/remove |
| `actions: write`                    | repo  | `PUT /repos/:o/:r/actions/permissions`                                                                  |
| `secrets: write`                    | repo  | repository Actions secrets (delete only — values come from a separate store)                            |
| `actions_variables: write`          | repo  | repository Actions variables (create/update/delete)                                                     |
| `environments: write`               | repo  | environment lifecycle + protection rules                                                                |
| `deployments: write`                | repo  | required by GitHub for environment writes                                                               |
| `pages: write`                      | repo  | GitHub Pages settings                                                                                   |
| `repository_hooks: write`           | repo  | repository webhooks (`/repos/:o/:r/hooks`)                                                              |
| `repository_custom_properties: write` | repo  | reading and writing custom-property values on repos                                                     |
| `contents: read`                    | repo  | reading `.github/settings/*.yml` from the default branch and PR head                                    |
| `metadata: read`                    | repo  | mandatory for any App installation; also required by GraphQL prefetch                                   |
| `checks: write`                     | repo  | posting the dry-run PR check on `pull_request.opened`/`synchronize`                                     |
| `pull_requests: write`              | repo  | optional PR-comment summary; required when posting check details                                        |
| `issues: read`                      | repo  | dependency of `pull_requests` permission family in GitHub's model                                       |
| `members: write`                    | org   | `PUT/DELETE /orgs/:org/teams/:slug/repos/...` to bind/unbind teams                                      |
| `organization_administration: read` | org   | resolving installation-by-owner via `/orgs/:org/installation`                                           |
| `organization_custom_properties: admin` | org | required to read property *definitions* alongside per-repo values                                       |

### Webhook events

| Event           | Action(s)                                | Effect                                                       |
| --------------- | ---------------------------------------- | ------------------------------------------------------------ |
| `push`          | (any)                                    | Reconcile when the default branch updates                    |
| `pull_request`  | `opened`, `synchronize`, `reopened`      | Dry-run reconcile + upsert sticky diff/error comment         |
| `pull_request`  | `closed` (merged only)                   | Reconcile against the merged base                            |
| `issue_comment` | `created` (PRs only)                     | Parse `@repo-settings <verb>` and dispatch the command       |
| `repository`    | (any)                                    | Re-evaluate when a repo is created, archived, or transferred |

### Where the secrets land

After the manifest exchange GitHub returns the App ID, PEM, and webhook
secret. Wire them in via the env vars documented above:

| From manifest exchange | Env var                       |
| ---------------------- | ----------------------------- |
| `id`                   | `GITHUB_APP_ID`               |
| `pem`                  | written to `GITHUB_APP_PRIVATE_KEY_PATH` |
| `webhook_secret`       | `GITHUB_WEBHOOK_SECRET`       |

## PR commands

When the App is installed on a repo, contributors can drive the bot from
inside a PR — Dependabot-style. Mention the App by its slug at the start
of a comment line, optionally followed by a verb:

| Comment                       | Effect                                                              |
| ----------------------------- | ------------------------------------------------------------------- |
| `@repo-settings recheck`      | Re-run the dry-run and refresh the sticky diff comment.             |
| `@repo-settings diff`         | Alias of `recheck`.                                                 |
| `@repo-settings apply`        | Apply the configuration now. Requires write access (`OWNER`, `MEMBER`, or `COLLABORATOR`); contributors get a polite refusal comment instead. |
| `@repo-settings help`         | Post the command list. Also fires when a known slug appears on a line with no verb. |
| `/repo-settings <verb>`       | Slash variant — same set of verbs.                                  |

Comment lines that *don't* start with the trigger are ignored, so prose
like "thanks @repo-settings — looks good" or `> @repo-settings apply`
(quoted reply) won't fire commands.

The bot maintains one **sticky comment** per PR, tagged with an
invisible HTML marker (`<!-- repo-settings:sticky -->`). Reruns update
that comment in place rather than stacking new ones. The sticky carries
either:

- the rendered diff table (no changes / N planned changes), or
- a list of validation errors when `.github/settings/*.yml` can't be
  parsed at the PR's head SHA, with the offending file names and the
  exact issues to fix.

## Disabling resources

Operators can refuse to manage specific resource types by setting
`DISABLED_RESOURCES` to a comma-separated list. The service still
accepts the YAML; it just never reads or writes those resources from
GitHub. Useful when a deployment shouldn't touch certain settings for
compliance reasons (e.g. secrets stored elsewhere, pages locked down
at the org level).

```env
DISABLED_RESOURCES=pages,secrets,deploy_keys
```

Recognised keys (snake_case canonical, hyphens accepted as aliases):

```
repository  teams      rulesets   environments  webhooks
autolinks   actions    security   pages         secrets
variables   deploy_keys (or deploy-keys)
custom_properties (or custom-properties)
collaborators  branches
```

Disabling `repository` covers both repo settings and topics, since
they ride the same Phase A lane.

### What users see

When `secrets` is disabled but a repo's `secrets.yml` still defines
secrets, the user gets explicit feedback in three places, not silence:

1. **PR sticky comment / `/api/check`** — a "Skipped — operator policy"
   section above the diff, listing each disabled-but-configured
   resource with the reason `disabled by operator policy`.
2. **`/api/validate`** — per-file `disabled: ["secrets"]` warnings plus
   a top-level rollup. Validation **does not fail** (the YAML is
   structurally fine); the section is just inert.
3. **Reconcile report / Prometheus** — one `Result{action: "skipped",
   success: true, error: "disabled by operator policy"}` per disabled
   lane; metric `applier_total{action="skipped",status="success"}`
   so dashboards can chart it.

The reconciler **never reads live state** for disabled lanes — no
GitHub API calls, no permission checks. The repo's YAML stays in
place so when the operator re-enables the resource, it just resumes
working without the user having to redo their config.

Unknown keys in `DISABLED_RESOURCES` log a warning at boot rather than
silently mis-spelling the gate; check the startup logs after edits.

## Driving via GitHub Actions

[`examples/workflow-pr-check.yml`](examples/workflow-pr-check.yml) is a
drop-in that talks to the service from inside the target repo. Two
trigger paths, each routed to the right API:

| Trigger             | Calls            | What runs                                                                  |
| ------------------- | ---------------- | -------------------------------------------------------------------------- |
| `pull_request`      | `/api/check`     | Dry-run against the PR head; posts the diff/error as a sticky PR comment; fails the check on lane errors. Skipped on fork PRs (secrets aren't injected). |
| `workflow_dispatch` | `/api/reconcile` | Maintainer chooses `dry-run` or `apply` against the default branch. Apply exits non-zero if any lane failed. |

Required repo secrets: `REPO_SETTINGS_URL` (base URL of the service)
and `REPO_SETTINGS_TOKEN` (matching `API_TOKEN` on the service). No
per-repo App credentials.

> **Heads-up:** if the GitHub App is installed on the same repo it will
> *also* post a sticky comment on PR open/sync (using a different
> marker, so the two don't collide — you'll just see two comments).
> Pick one source of truth and uninstall/disable the other.

## Local development

```bash
make ci            # vet + fmt-check + lint + race tests + build
make test-race     # race-detector test run
make coverage      # coverage profile
make docker        # build container image locally
```

## Metrics

Names match the original TypeScript service so existing dashboards and
alerts continue to work.

| Metric                       | Type      | Labels                       |
| ---------------------------- | --------- | ---------------------------- |
| `reconcile_total`            | counter   | `trigger`, `status`          |
| `reconcile_duration_seconds` | histogram | `trigger`                    |
| `applier_total`              | counter   | `resource`, `action`, `status` |
| `rate_limit_remaining`       | gauge     | `pool`                       |
| `queue_size`                 | gauge     | `priority`                   |
| `api_calls_total`            | counter   | `method`, `endpoint`, `status` |
| `webhook_events_total`       | counter   | `event`, `action`            |
| `policy_violations_total`    | counter   | `severity`, `field`          |
| `pr_check_total`             | counter   | `result`                     |

## Differences from the TS service

- **No Probot.** Plain `net/http.ServeMux` + a `go-github`-free typed
  client. Webhooks and HMAC verification are first-party.
- **No standalone ETag cache.** Conditional requests live inside an
  `http.RoundTripper` middleware so every cacheable GET benefits without
  per-call boilerplate.
- **Org/suborg layering** is wired in `internal/config` (`Resolve`,
  `SuborgMatch`) but the entry point currently passes only the per-repo
  layer. Wire org/suborg loaders in `cmd/repo-settings/main.go` once your
  admin repo is in place.
- **Stress tooling and PR-check formatting** are out of scope for this
  port.

See `plan.md` for the redesign notes that drove these choices.

## License

[MIT](LICENSE)

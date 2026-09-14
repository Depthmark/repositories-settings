# repositories-settings

Declarative GitHub repository configuration-as-code. Reconciles per-repo
YAML configs in `.github/settings/` against live GitHub state and applies
the diff. Go port of [`repository-settings`](https://github.com/Depthmark/repository-settings)
(TypeScript / Probot).

This README is the current operating contract. The
[architecture decision](docs/adr/0001-reconciliation-boundaries.md) records
the trust boundaries and migration, and the
[remediation report](.agents-task/remediation-report.md) separates local
verification from the staging checks still required. The `plan*.md`
documents are historical design proposals.

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
  GraphQL prefetch. The package is implemented and tested, but
  `cmd/repo-settings` does not construct it: nothing schedules a batch
  run today. Wire it in `main.go` when you want one.

## Architecture

```
Trigger (webhook / API)
        │
        ▼
 Settings resolution           org layer → suborg tiers → repo layer
        │
        ▼
 Reconciler.Reconcile          per-repo, one unit of work, under the
        │                       per-repository lock
        ▼
 Org policy gate               error-severity violations refuse the run
        │                       before applier live-state reads or writes
        ▼
 Phase A: repo + topics         runs before the remaining lanes
        │
        ▼
 Phase B — 14 lanes in parallel, joined by a sync.WaitGroup
   teams · rulesets · environments · webhooks · autolinks ·
   actions · security · pages · secrets · variables · deploy_keys ·
   custom_properties · collaborators · branches
        │
        └─ each lane: GetLive → Plan → Apply (bounded fan-out, n=5)
```

Concurrency is bounded at three independent levels, and the innermost
limit is not a global one: Phase B runs up to 14 lanes at once, each
named-list lane fans out at most 5 mutations, and the shared HTTP
scheduler caps in-flight wire requests at 10.

### Key invariants

- **Per-lane error isolation.** A lane failure produces a single
  synthetic `applier.Result` with `Success: false`; the rest of the
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
  body on `304 Not Modified`. A successful write drops every cached read
  on a related path — the write target, anything beneath it, and the
  collection a written item belongs to — so creating a ruleset
  invalidates the ruleset list.
- **Policy gates the appliers.** When an org admin repository is
  configured, an error-severity violation stops the reconcile before
  Phase A. A partial apply of the parts that happened to be legal is
  exactly what the policy exists to prevent. Configuration loading and
  API authorization can read GitHub before this gate.
- **Typed drift survives comparison.** The diff widens numbers (a JSON
  round-trip makes every number a float64) and coerces nothing else, so
  a live `"true"` is drift from a desired `true` rather than a match.

## Layout

```
cmd/repo-settings/        entry point (DI wiring, signal handling)
internal/
  config/                 schema, validation, loader, Normalize, org/suborg
                          resolution, admin policy, selective-reconcile mapping
  oidc/                   OIDC verification: JWKS discovery and caching,
                          GitHub Actions repository identity
  yamlstrict/             single-document, known-field YAML decoding
  diff/                   pure-logic deep-compare + named-list / single-
                          resource diff + markdown formatting
  conc/                   bounded fan-out helper
  ghclient/               transport chain (rate limit -> conditional GET ->
                          App auth -> metrics) with go-github mounted on it
  ghapi/                  typed boundary: route templates + per-resource
                          encode/decode between our model and the SDK
  applier/                repository/topics plus 14 Phase B lane factories
  reconciler/             phased orchestrator
  worker/                 cron batch with GraphQL prefetch
  server/                 HTTP server (webhook, /api/*, /healthz, /readyz,
                          /metrics) + middleware
  metrics/                Prometheus registry
  logger/                 slog construction + helpers
docs/adr/                 current architecture decisions
plan.md                   historical port plan + redesign notes
```

## Configuration

Environment variables. The service **fails to start** when a required
one is missing rather than degrading to an insecure default: an
unauthenticated ingress and an anonymous GitHub client both fail in ways
that look like success (a private repo returns 404, the loader reads that
as "unmanaged", and every PR check passes with 0 diffs).

### Required

| Var               | Purpose                                                                 |
| ----------------- | ----------------------------------------------------------------------- |
| `APP_ID`          | GitHub App ID                                                            |
| `PRIVATE_KEY` or `PRIVATE_KEY_PATH` | The App's PEM, inline or as a file path. `PRIVATE_KEY` wins when both are set. |
| `WEBHOOK_SECRET`  | HMAC secret. `/webhook` rejects every payload without a valid `X-Hub-Signature-256`. |
| `OIDC_AUDIENCE` or `API_TOKEN` | At least one privileged-API credential. See [API authentication](#api-authentication). |

`ALLOW_UNAUTHENTICATED=1` permits missing credentials for local development.
With no webhook secret, unsigned webhooks are accepted; with no API
credential, privileged API calls are accepted; with no `APP_ID`, the
GitHub client runs anonymously. Supplied webhook secrets and static
tokens are still checked, and a supplied App ID still requires a valid
private key. Leave this switch unset in deployed environments.

### Optional

| Var                    | Default                  | Purpose                                                    |
| ---------------------- | ------------------------ | ---------------------------------------------------------- |
| `ADDR`                 | `:8080`                  | HTTP listen address                                        |
| `LOG_LEVEL`            | `info`                   | `debug` / `info` / `warn` / `error`                        |
| `GITHUB_API_URL`       | `https://api.github.com` | GitHub Enterprise Server users override                    |
| `APP_SLUG`             | `repo-settings`          | The bot's @-name; used to recognise PR-comment commands    |
| `ORG_ADMIN_REPO`       | (none)                   | Org admin repository. `owner/name` pins one; a bare `name` resolves inside each target's own owner. Unset means no org defaults and no policy enforcement. See [Org layering and policy](#org-layering-and-policy). |
| `DISABLED_RESOURCES`   | (none)                   | CSV of resource keys the deployment refuses to manage (see [Disabling resources](#disabling-resources)) |
| `OIDC_ISSUERS`         | `https://token.actions.githubusercontent.com` | CSV of trusted OIDC issuers. Must be `https`. |
| `OIDC_TRUSTED_JWKS_HOSTS` | (none)                | Whitespace-separated `issuer=host[,host]` exceptions; commas separate hosts within one entry. The default pins JWKS to the issuer host. |
| `OIDC_REQUIRE_IMMUTABLE_SUBJECT` | `0`            | `1` rejects an Actions token whose subject omits the numeric owner and repository IDs. |

## API authentication

The privileged `/api` routes accept two credentials. `/api/validate` is
unauthenticated: it parses YAML it is handed and reads nothing.

**OIDC (recommended).** Set `OIDC_AUDIENCE` and have workflows call with
the identity token GitHub mints for them. The token's claims name the
repository the workflow runs in, and the service will only act on that
repository; a request for any other returns `403`. `/api/check` and
`/api/reconcile` with `dry_run: true` accept repository-scoped workflow
identities. A real apply additionally requires a `push`,
`workflow_dispatch`, or `schedule` event, a token `ref` naming the current
default branch, and numeric repository and owner IDs matching metadata
fetched from GitHub. Pull request and feature-branch tokens cannot apply.
Metadata lookup failures refuse the apply with `502`.

The token's signature is checked against the issuer's JWKS, discovered
over HTTPS with redirects refused and the `jwks_uri` pinned to the
issuer's own host. `RS256` and an expiry are required; the audience must
match; a key is selected only by `kid`. The repository is then
established from three independent signed claims — `repository`,
`repository_owner`, and `sub` — which all have to agree. Signed numeric
`repository_id` and `repository_owner_id` claims are required too; real
applies additionally compare them with current GitHub metadata.

**Static token.** Set `API_TOKEN` and send it as a bearer. It is not
repository-scoped: whoever holds it can reconcile every repository the
App is installed in. Use it only where OIDC is unavailable.

Both may be configured at once. When OIDC is configured, a bearer shaped
like a JWT is checked by the OIDC verifier and never falls back to
`API_TOKEN` after rejection. Static tokens use a constant-time comparison.

## Org layering and policy

Set `ORG_ADMIN_REPO` to layer organization-wide defaults under every
repository's own configuration and to enforce an admin policy on the
result. The admin repository uses the same file names and envelopes as a
managed repository:

```
.github/settings/*.yml               org-wide defaults
.github/settings/policy.yml          admin policy (locked fields, ceilings)
.github/settings/suborgs.yml         suborg tiers and what each matches
.github/settings/suborgs/<name>/*.yml   one tier's overrides
```

Layers resolve org → matching suborgs → repository, last writer winning
per resource. The merged result is then validated against the policy.
An error-severity violation refuses the reconcile before Phase A and is
reported in the PR comment; a warning is reported and does not block.
The repository's own layer is kept separate from the merge so the policy
can tell an override apart from an inherited value.

Each file is optional. An admin repository with no `policy.yml` is a
pure defaults layer; one with only `policy.yml` constrains repositories
without contributing settings. A configured admin repository must exist
and be readable by the App. Missing access, invalid configuration, or a
failed read stops reconciliation; it does not disable policy. Malformed
`ORG_ADMIN_REPO` values fail startup. The admin repository never governs
itself.

The executable supplies repository identity for suborg matching, so use
`match.repos` with repository-name or `owner/name` globs. The schema also
supports `teams` and `custom_properties`, but `main` does not fetch those
attributes: a tier requiring either cannot match in this executable.

The admin layer is cached for five minutes. Policy/default updates can
therefore take up to five minutes to affect later runs; failed loads are
retried by the next caller. There is no `/api/invalidate-cache` endpoint.

## Endpoints

| Path                  | Auth                                | Purpose                                       |
| --------------------- | ----------------------------------- | --------------------------------------------- |
| `POST /webhook`       | `X-Hub-Signature-256` (HMAC SHA256) | GitHub event intake (`push`, `pull_request`, `issue_comment`) |
| `POST /api/reconcile` | Bearer; OIDC applies also require a trusted event and the current default branch | Manual reconcile (`{owner, repo, dry_run, changed_files}`) |
| `POST /api/check`     | Bearer, repository-scoped under OIDC | Workflow dry-run (`{owner, repo, head_sha, pr_number}`), returns a conclusion |
| `POST /api/validate`  | none                                | Validate YAML files (`{files: [{name, content}]}`). `200` when valid, `422` when not. |
| `GET /healthz`        | none                                | Liveness                                      |
| `GET /readyz`         | none                                | Readiness (returns 503 during shutdown)       |
| `GET /metrics`        | none                                | Prometheus metrics                            |

`/webhook` handles three events. A `push` reconciles only when the
pushed ref is the repository's own default branch, as reported by
GitHub in the event payload; a push to any other branch is acknowledged
and ignored, so a contributor cannot apply settings by pushing a branch.
Feature-branch configuration is evaluated through the pull-request
dry-run path instead. There is no `repository` event handler.

`/api/reconcile` always loads the default branch; omitting `dry_run`
requests a real apply. `/api/check` uses `head_sha` when supplied and
returns `success` for no drift, `neutral` for actionable drift, or
`failure` for a lane error or blocking policy violation. Reconcile reports
can return HTTP `200` with failed results: inspect `blocked` and
`applied[].success`, not only the HTTP status. `/api/validate` checks file
structure and supported fields; it does not resolve org policy or inspect
live GitHub state.

All reconciles, including dry runs, serialize per repository through
`Reconciler.Reconcile`. The lock is shared by callers using the same
GitHub client in one process. It does not coordinate separate replicas
or deduplicate webhook deliveries. Intake still runs synchronously in
the HTTP request. `/readyz` reports process readiness, not GitHub access
or policy availability.

## GitHub App registration

Register the App manually for a deployment that has no separate manifest
registration service:

1. In the owning organization's **Settings > Developer settings > GitHub
   Apps**, create an App. Set its webhook URL to
   `https://repo-settings.example.com/webhook` and choose a webhook secret.
   Set the permissions and event subscriptions listed below. See
   [GitHub's registration instructions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app).
2. Record the App ID and generate a private key on the App's settings
   page. Store the downloaded PEM in the deployment's secret store. See
   [GitHub's private-key instructions](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps).
3. Configure `APP_ID`, `PRIVATE_KEY_PATH` or `PRIVATE_KEY`, and
   `WEBHOOK_SECRET` on the service, plus an API credential as described
   above. Install the App on the target repositories and the configured
   admin repository.

The following manifest is a reference for registration tooling. This
service has no manifest callback route. To automate registration, a
separate handler must receive `redirect_url`, validate `state`, and
exchange the returned code using
`POST /app-manifests/{code}/conversions` within one hour. Pointing the
redirect at GitHub's settings page does not perform that exchange. See
the [manifest handshake](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).

```json
{
  "name": "repositories-settings",
  "url": "https://github.com/Depthmark/repositories-settings",
  "description": "Declarative GitHub repository configuration-as-code. Reconciles .github/settings/*.yml against live repo state.",
  "public": false,
  "redirect_url": "https://registration.example.com/github-app/callback",
  "hook_attributes": {
    "url": "https://repo-settings.example.com/webhook",
    "active": true
  },
  "default_events": [
    "push",
    "pull_request",
    "issue_comment"
  ],
  "default_permissions": {
    "administration": "write",
    "actions": "write",
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
| `pull_requests: write`              | repo  | the sticky dry-run comment on `pull_request.opened`/`synchronize` and the reply to a PR command          |
| `issues: read`                      | repo  | dependency of `pull_requests` permission family in GitHub's model                                       |
| `members: write`                    | org   | `PUT/DELETE /orgs/:org/teams/:slug/repos/...` to bind/unbind teams                                      |
| `organization_administration: read` | org   | resolving installation-by-owner via `/orgs/:org/installation`                                           |
| `organization_custom_properties: admin` | org | required to read property *definitions* alongside per-repo values                                       |

### Webhook events

| Event           | Action(s)                                | Effect                                                       |
| --------------- | ---------------------------------------- | ------------------------------------------------------------ |
| `push`          | (any)                                    | Reconcile when — and only when — the pushed ref is the repository's default branch |
| `pull_request`  | `opened`, `synchronize`, `reopened`      | Dry-run reconcile + upsert sticky diff/error comment         |
| `pull_request`  | `closed` (merged only)                   | Reconcile the repository's current default branch            |
| `issue_comment` | `created` (PRs only)                     | Parse `@repo-settings <verb>` and dispatch the command       |

Every other event is acknowledged and ignored. There is no `repository`
handler: a repository created, archived, or transferred is picked up by
the next push or manual run, not by the event itself.

### Where the secrets land

After the manifest exchange GitHub returns the App ID, PEM, and webhook
secret. Wire them in via the env vars documented above:

| From manifest exchange | Env var                                            |
| ---------------------- | -------------------------------------------------- |
| `id`                   | `APP_ID`                                           |
| `pem`                  | `PRIVATE_KEY` inline, or written to `PRIVATE_KEY_PATH` |
| `webhook_secret`       | `WEBHOOK_SECRET`                                   |

## PR commands

When the App is installed on a repo, contributors can drive the bot from
inside a PR — Dependabot-style. Mention the App by its slug at the start
of a comment line, optionally followed by a verb:

| Comment                       | Effect                                                              |
| ----------------------------- | ------------------------------------------------------------------- |
| `@repo-settings recheck`      | Dry-run the current default-branch configuration and refresh the sticky diff comment. |
| `@repo-settings diff`         | Alias of `recheck`.                                                 |
| `@repo-settings apply`        | Apply the current default-branch configuration. The signed webhook must report `OWNER`, `MEMBER`, or `COLLABORATOR`; other associations are refused. |
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

## What this service does not manage

A repository has far more configuration than this service reconciles.
[`docs/ui-only-settings.md`](docs/ui-only-settings.md) is the full
inventory of what stays in the GitHub UI, with the reason for each —
whether it has no API, is deliberately out of scope, belongs to the
organization rather than the repository, or is simply not wired up yet.

Two things it is worth knowing before reading anything else:

- A scalar setting you never mention in YAML is left exactly as it is.
- A declared **collection** is authoritative: a webhook, autolink, deploy key,
  variable, team binding, collaborator, environment or ruleset that
  exists on GitHub but is absent from your YAML is deleted. So is any
  branch-protection sub-setting you omit from a branch you do list.
  An undeclared collection remains unmanaged.

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

[`examples/workflow-pr-check.yml`](examples/workflow-pr-check.yml) shows
static-token calls from a target repository. Before enabling it, create
a GitHub environment named `repo-settings` with required reviewers and
trusted deployment branches. Store `REPO_SETTINGS_URL` and
`REPO_SETTINGS_TOKEN` as environment secrets; the token must match
the service's `API_TOKEN`. Keep this operator-wide token out of repository
or organization secrets available to unreviewed workflow changes.
Both jobs require that protected environment. Two trigger paths call
the API:

| Trigger             | Calls            | What runs                                                                  |
| ------------------- | ---------------- | -------------------------------------------------------------------------- |
| `pull_request`      | `/api/check`     | Dry-run against the PR head; posts the diff/error as a sticky PR comment; fails the check on lane errors. Skipped on fork PRs (secrets aren't injected). |
| `workflow_dispatch` | `/api/reconcile` | Maintainer chooses `dry-run` or `apply`; the job runs only from the default branch. Apply exits non-zero if any lane failed. |

HTTP errors fail the sample jobs, and the pull request job accepts only
the defined `success`, `neutral`, or `failure` conclusions. Its sticky
comment reads the rendered summary from a file.

Use the maintained reusable workflow below for OIDC authentication,
which needs no shared service secret:

```yaml
name: repo-settings
on:
  push:
    branches: [main] # Replace with this repository's default branch.
    paths: ['.github/settings/**']
  pull_request:
    paths: ['.github/settings/**']
permissions:
  contents: read
  pull-requests: write
  id-token: write
jobs:
  settings:
    uses: Depthmark/repositories-settings/.github/workflows/repo-settings-default.yml@<verified-full-commit-sha>
    with:
      api-url: https://repo-settings.example.com
      oidc-audience: repo-settings
```

Replace `<verified-full-commit-sha>` with a reviewed commit containing
both reusable workflow files before committing this example. Set the
service's `OIDC_AUDIENCE=repo-settings`, or change both audience values
to the same string.

The wrapper defaults to `auth-mode: oidc`, so the run authenticates with
its own GitHub identity token. `id-token: write` has to be granted by the
calling workflow, because a reusable workflow can only narrow the
permissions it is given; the permission alone confers no repository
access. The low-level `repo-settings.yml` also defaults to
`auth-mode: oidc`. Token mode requires `secrets.api-token` matching the
service's `API_TOKEN`; it also works when the service enables OIDC.
`auth-mode: sts` is no longer supported.

The reusable workflow checks pull requests from the same repository;
fork contributions use the App webhook check. Its apply step runs only
for a non-deleted push to the repository's default branch.

The webhook handler and workflow each post their own sticky comment.
Choose which integration handles pull request checks to avoid duplicate
comments. Keep the GitHub App installed: the service needs its
installation credentials for workflow-driven reads and writes too.

## Migrating an existing deployment

1. Build with Go 1.26.7 or newer and the digest-pinned images in
   `Dockerfile`. Set `APP_ID`, a valid App private key, and
   `WEBHOOK_SECRET`; leave `ALLOW_UNAUTHENTICATED` unset.
2. Choose the API credential. For OIDC, set `OIDC_AUDIENCE` to match
   `oidc-audience`, grant callers `id-token: write`, and replace old STS
   inputs with `auth-mode: oidc`. Retain `API_TOKEN` only for clients
   that still require a static operator credential.
3. Restrict workflow apply triggers to the repository's default branch.
   Use `/api/check` for pull request heads. Update consumers to handle
   validation HTTP `422`, authorization HTTP `401`/`403`, and failure
   fields in HTTP `200` reconcile reports.
4. If org policy is required, set a valid `ORG_ADMIN_REPO`, install the
   App there, and confirm it contains the intended `policy.yml` and
   defaults. Allow five minutes for cached admin changes to expire;
   leaving the variable unset intentionally disables org enforcement.
5. Run the [staging verification cases](docs/adr/0001-reconciliation-boundaries.md#staging-verification)
   in a disposable repository before enabling production mutation.
   Keep one active process for overlapping repository ownership until
   deployment-level coordination is provided.

## Local development

Use Go 1.26.7 or newer. `make ci` also needs `golangci-lint`; tests use
local HTTP listeners. A development shell must permit loopback sockets.

```bash
make ci            # vet + fmt-check + lint + race tests + build
make test-race     # race-detector test run
make coverage      # coverage profile
make docker        # build container image locally
```

## Metrics

The service exports the following metrics. Validate existing dashboards
against these names and labels; this remediation did not run a
TypeScript parity harness.

| Metric                       | Type      | Labels                       |
| ---------------------------- | --------- | ---------------------------- |
| `reconcile_total`            | counter   | `trigger`, `status`          |
| `reconcile_duration_seconds` | histogram | `trigger`                    |
| `applier_total`              | counter   | `resource`, `action`, `status` |
| `rate_limit_remaining`       | gauge     | `pool`                       |
| `queue_size`                 | gauge     | `priority`                   |
| `api_calls_total`            | counter   | `method`, `route`, `status` |
| `webhook_events_total`       | counter   | `event`, `action`            |
| `policy_violations_total`    | counter   | `severity`, `field`          |
| `pr_check_total`             | counter   | `result`                     |
| `oidc_validations_total`     | counter   | `issuer`, `result`           |
| `api_authorizations_total`   | counter   | `kind`, `result`             |

## Differences from the TS service

- **No Probot.** Plain `net/http.ServeMux`; webhooks and HMAC
  verification are first-party.
- **GitHub calls go through [`google/go-github`](https://github.com/google/go-github).**
  The SDK is mounted on our own `http.Client`, so it inherits the
  two-pool rate limiter, the conditional-GET cache, installation-token
  auth and metrics without knowing they exist. Per-call priority, owner
  and route template travel in the request context (`ghclient.WithCall`),
  which is the only channel the SDK forwards untouched.

  Two resources stay on the raw REST path deliberately. **Rulesets**,
  because the SDK models rules as a typed union — one field per rule kind
  it knows about — while GitHub documents them as a `type` plus a
  free-form `parameters` map. Round-tripping through the union would
  silently drop any rule the installed SDK version has not learned yet,
  which for a policy tool is the worst case: the dry-run reports the rule
  as absent and the apply deletes it. And **private vulnerability
  reporting**, which the SDK can enable and disable but not read.
- **No standalone ETag cache.** Conditional requests live inside an
  `http.RoundTripper` middleware so every cacheable GET benefits without
  per-call boilerplate.
- **Strict config parsing.** `.github/settings/*.yml` is decoded with
  `KnownFields(true)`, so a misspelled key is an error naming the field
  and its line rather than a setting that silently does nothing.
- **Org/suborg layering and admin policy are enforced**, not merely
  present. Set `ORG_ADMIN_REPO` to turn them on; see
  [Org layering and policy](#org-layering-and-policy). With it unset, a
  repository's own configuration stands alone and no policy applies.
- **Workflows authenticate with their own GitHub identity.** The
  reusable workflow's `auth-mode: oidc` sends the short-lived token
  GitHub mints for the run, which names the repository it runs in, so
  the service refuses a request for any other. See
  [API authentication](#api-authentication).
- **The cron worker is not scheduled.** `internal/worker` is implemented
  and tested but `cmd/repo-settings/main.go` never constructs it.
- **Stress tooling** is out of scope for this port.

See the [architecture decision](docs/adr/0001-reconciliation-boundaries.md)
for current boundaries. `plan.md` preserves the earlier redesign proposals.

## License

[MIT](LICENSE)

# ADR 0001: Reconciliation trust and execution boundaries

Status: implemented in the current working tree; live staging verification is outstanding.

Audience: maintainers, deployment operators, and security reviewers.

## Context

The [2026-08-25 architecture review](../../.agents-task/codebase-architecture-review.md)
identified branch trust, unauthenticated ingress, workflow credentials,
policy enforcement, and serialization as release blockers. The service
holds GitHub App permissions across installed repositories, so each
trigger must establish what it may reconcile before invoking the appliers.
The [README](../../README.md) is the current deployment and API contract.

## Trigger scope

| Entry point | Configuration loaded | Mutation authority |
|---|---|---|
| Signed `push` webhook | Default branch named in the payload | Apply only when `ref` equals `refs/heads/<default_branch>`; absent default-branch metadata and other refs are ignored |
| Signed pull request `opened`, `synchronize`, `reopened` webhook | Pull request head SHA | Dry run; the App posts the report |
| Signed merged pull request webhook | Current default branch | Apply current default-branch settings |
| Signed pull request comment `recheck` or `diff` | Current default branch | Dry run; this command does not load the pull request head |
| Signed pull request comment `apply` | Current default branch | Requires the payload association `OWNER`, `MEMBER`, or `COLLABORATOR`; this path uses the signed webhook association, not a workflow identity |
| `POST /api/check` | Requested `head_sha`, or default branch if omitted | Always dry run |
| `POST /api/reconcile` | Current default branch | `dry_run: true` plans only; false or omitted requests an apply subject to API authorization |

There is no `repository` event handler. Both merged pull request and push
events may request reconciliation for the same change. There is no
delivery-ID deduplication or exactly-once guarantee.

## Direct OIDC and static API credentials

GitHub Actions workflows send the GitHub identity token directly to this
service. An STS installation token is not a supported service credential.
`OIDC_AUDIENCE` must match the workflow's `oidc-audience`; both reusable
workflows default the latter to `repo-settings` and default `auth-mode`
to `oidc`. The caller grants `id-token: write`. The wrapper requires
`api-url`; the low-level workflow defaults it to
`https://repo-settings.internal`. Token mode instead requires the
`api-token` secret matching `API_TOKEN`.

The verifier requires a trusted HTTPS issuer, the configured audience,
RS256 signature, expiration, and a named JWKS key. Discovery rejects
redirects and normally restricts `jwks_uri` to the issuer host. The
`repository`, `repository_owner`, and `sub` claims must agree. Operators
must supply tokens with signed numeric `repository_id` and
`repository_owner_id` claims. They can also require subjects containing
immutable numeric IDs with
`OIDC_REQUIRE_IMMUTABLE_SUBJECT=1`. Off-host JWKS exceptions use
`OIDC_TRUSTED_JWKS_HOSTS`: whitespace separates `issuer=host[,host]`
entries, and commas separate hosts within an entry.

Repository scope alone authorizes dry runs. An OIDC apply additionally
requires `event_name` to be `push`, `workflow_dispatch`, or `schedule`;
`ref` must match the current default branch returned by GitHub REST; and
the signed repository and owner IDs must equal the current REST IDs.
This prevents a pull request or feature-branch identity from applying
and rejects a token for a repository name that has since changed owners
or been reused. A metadata lookup failure returns `502`; a failed event,
ref, or identity check returns `403`. This is repository and event trust,
not an allowlist of individual workflow files.

`API_TOKEN` remains an operator credential with authority across every
repository available to the App. Its holders do not receive the OIDC
branch/event restriction. Both mechanisms can coexist during migration.
With OIDC configured, JWT-shaped bearers are validated as OIDC and cannot
fall back to the static secret after rejection. Static comparisons use
SHA-256 digests and constant-time equality.

The [static-token example](../../examples/workflow-pr-check.yml) requires
a GitHub environment named `repo-settings` with required reviewers and
trusted deployment branches. Its `REPO_SETTINGS_URL` and
`REPO_SETTINGS_TOKEN` credentials belong in environment secrets;
approval controls access to the operator-wide token. It skips fork pull
requests, gates manual jobs to the default branch, fails HTTP errors,
and validates dry-run conclusions. The OIDC reusable workflow avoids
distributing that shared service credential.

Startup requires a webhook secret, an API credential, and valid App
credentials. `ALLOW_UNAUTHENTICATED=1` permits missing credentials for
local development, while supplied credentials still undergo their normal
checks. `/api/validate` deliberately remains public: it validates supplied
files under a 1 MiB request limit and does not read repository state.
`/healthz`, `/readyz`, and `/metrics` also remain unauthenticated. The
deployment must choose their external exposure; readiness does not prove
GitHub connectivity or policy access.

Implementation: [startup](../../cmd/repo-settings/main.go),
[API authorization](../../internal/server/auth.go),
[OIDC verifier](../../internal/oidc/oidc.go), and
[identity binding](../../internal/oidc/github.go).

## Reconciler owns serialization

Every trigger calls `Reconciler.Reconcile`, which acquires the client's
case-insensitive `owner/name` lock before the policy gate and both
reconciliation phases. This includes dry runs, so a dry run does not
observe a partly applied state from another call using the same client.
Callers must not acquire this lock around `Reconcile` themselves because
it is not reentrant. Desired configuration is resolved before acquiring
the lock; GitHub configuration reads are not an atomic repository snapshot.

The lock coordinates one client in one process. It is not a distributed
lock, does not deduplicate requests, and does not provide cancellation
while waiting to acquire it. Its per-repository entries are retained.
Run one active process for a given set of repositories until deployment
coordination is implemented. Different repositories can run concurrently.

Phase A runs repository settings and topics before Phase B. Phase B has
14 parallel lanes joined with `sync.WaitGroup`; named-list mutation
fan-out is at most five per lane, and the shared HTTP scheduler permits
ten in-flight requests. This order is not a transaction: lane failures
are reported, and independent lanes can still run.

Implementation: [reconciler](../../internal/reconciler/reconciler.go),
[lock](../../internal/ghclient/repo_lock.go), and
[server entry helper](../../internal/server/reconcile.go).

## Optional organization policy

An unset `ORG_ADMIN_REPO` intentionally enables repository-only
configuration. A valid value names `owner/name`, or a bare name resolved
within each target repository's owner. When configured, the App must be
able to read that repository. Missing or inaccessible repositories,
failed reads, and invalid YAML stop resolution and therefore stop apply.
Malformed references fail startup.

Within a readable admin repository, defaults, `policy.yml`, and
`suborgs.yml` are optional. Defaults-only and policy-only repositories
are supported. An empty accessible admin repository supplies no defaults
or policy. The admin repository does not govern itself. Policy authors
must protect that repository through their normal repository controls.

Resolution merges org settings, matching suborg tiers in declaration
order, and repository settings. Repository intent is retained separately
for override checks. Error-severity policy violations block before any
applier live-state reads or writes; warnings remain visible and do not
block. Selective resource filtering preserves the policy verdict.
Configuration and authorization reads can precede this gate.

The executable populates only repository identity for matching. Use
`match.repos` globs. `teams` and `custom_properties` criteria exist in the
schema, but tiers requiring them do not match without an integration
that populates those attributes.

The admin layer cache lasts five minutes. Existing cached policy remains
in use until expiry, including if access is revoked in that interval.
After expiry a failed load blocks that caller and is retried by the next
caller. There is no cache-invalidation endpoint. Changes to admin policy
therefore do not take effect immediately across every run.

Implementation: [admin loader](../../internal/config/org.go),
[cache](../../internal/config/admin_cache.go),
[resolution](../../internal/config/resolution.go), and
[policy tests](../../internal/reconciler/policy_test.go).

## Cron remains dormant

The `internal/worker` package supports periodic batches with GraphQL
prefetch. The executable does not construct or schedule it, and no
environment variable turns cron on. This remediation keeps that decision
explicit while correcting prefetch: repository fields absent from the
GraphQL shape or incomplete topic lists require REST fallback. Partial
records are not treated as complete live state.

Future activation requires an explicit scheduler integration, repository
enumeration and lifecycle decisions, a shared client/reconciler, and
live parity checks. Tests of the package do not demonstrate a running
cron deployment. Async webhook intake, delivery deduplication, report v1,
and schema publication also remain deferred proposals.

Implementation: [worker](../../internal/worker/worker.go) and
[prefetch boundary](../../internal/applier/repo.go).

## Migration

1. Build with Go 1.26.7 or newer and the verified digest-pinned images.
   Supply App credentials and `WEBHOOK_SECRET`, leave
   `ALLOW_UNAUTHENTICATED` unset, and keep one active process per owned
   repository set.
2. Set `OIDC_AUDIENCE` to the workflow audience and grant `id-token: write`.
   Replace old STS inputs with `auth-mode: oidc`; verify consumers using
   static tokens have `API_TOKEN` configured. Retain the App installation
   because it authorizes the service's outbound GitHub operations.
3. Pin the reusable workflow to a reviewed full commit SHA. Route same
   repository pull requests to `/api/check`; the maintained reusable
   workflow applies only on non-deleted default-branch pushes. Use the
   App webhook for fork pull request checks.
4. For org enforcement, configure a readable `ORG_ADMIN_REPO` with the
   intended policy, use supported repository matching, and account for
   the five-minute cache. Update clients for validation HTTP `422` and
   for blocked/failed results returned inside HTTP `200` reports.
5. Complete the staging cases below before enabling production mutation.
   The [local remediation report](../../.agents-task/remediation-report.md)
   records what has been verified without a deployed App.

## Staging verification

Use an identified staging service and a disposable repository with the
App installed. Record the service image digest, workflow commit, request
identity, and before/after GitHub state for each case.

| Case | Expected result |
|---|---|
| Feature-branch push or PR workflow token requests an apply | Webhook push ignored or OIDC API request refused; managed GitHub state unchanged |
| Default-branch workflow requests an apply | Correct audience, event, ref, and numeric IDs accepted; configured drift applied |
| Missing/invalid webhook or API credentials, wrong OIDC repository, renamed/recreated identity | Rejected without mutation; compare expected `401`, `403`, and metadata-error `502` behavior |
| Concurrent webhook/API reconcile for the same repository | Mutation phases serialize in the single service process; repeated deliveries are not assumed deduplicated |
| Policy violation or inaccessible configured admin repository | Apply blocked; test policy freshness after the five-minute cache expires |
| Item write followed by a collection read | Cached collection refreshes and reports current GitHub state |
| Worker integrated in a separate test harness | Prefetched reconciliation matches full REST for every configured field and truncated topics |

Live GitHub payload compatibility, installed App permissions, deployed
ingress, Helm/Flux settings, and the external `github-sts` service are
not verified by local unit tests. They remain separate staging or
deployment work.

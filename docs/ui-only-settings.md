# Settings you still have to change in the GitHub UI

This service manages a defined slice of a repository's configuration.
Everything outside that slice is unmanaged: the service will not read it,
will not write it, and will not warn you when it drifts. This page is the
inventory of that outside.

Read it as the answer to two questions:

- *"I changed something in the UI — will repo-settings revert it?"*
  Only if it appears in [What is managed](#what-is-managed). Anything on
  this page is safe from the reconciler, and equally, is not protected by
  it.
- *"Can I put this in `.github/settings/`?"* If it is on this page, no —
  and the reason is given, because the reasons differ and some are
  temporary.

Accurate as of go-github v76 and the GitHub REST API version
`2022-11-28`.

## How to read the reasons

| Reason | Meaning |
|---|---|
| **Not implemented** | GitHub exposes an API for it; this service has not wired it up yet. A candidate for a future release. |
| **No API** | GitHub offers it in the UI only. No tool can manage it declaratively — not this one, not Terraform, not safe-settings. |
| **Out of scope** | Deliberately excluded. The reason is given per row; these are not oversights. |
| **Org-level** | Real and API-addressable, but it belongs to the organization, not the repository. This service reconciles repositories. |
| **File, not setting** | Configured by committing a file to the repository, so it is already config-as-code — just not this tool's. |

A field marked **Not implemented** that you can nevertheless *write* in
YAML will be rejected at validation time with a message saying so. The
service never accepts configuration it will not act on: silently ignoring
a section is the failure mode this whole design exists to prevent.

---

## Settings → General

| Setting | Reason | Notes |
|---|---|---|
| Repository name (rename) | Out of scope | A rename breaks every clone, submodule, and hard-coded URL pointing at the repo. It should be a deliberate human act, not a side effect of merging a YAML change. |
| Repository creation and deletion | Out of scope | This service reconciles repositories that exist. Creation belongs to whatever provisions repos; deletion should never be one merged PR away. |
| Transfer ownership | Out of scope | Same reasoning as rename, with more blast radius. |
| Default branch | Not implemented | `PATCH /repos/{o}/{r}` accepts `default_branch`. Worth adding, with care: switching it while open PRs target the old branch retargets them. |
| Allow forking | Not implemented | `PATCH /repos/{o}/{r}` accepts `allow_forking`. A straightforward addition. |
| Social preview image | No API | Upload only. |
| Sponsorships (sponsor button) | No API | The `FUNDING.yml` file covers the links; the toggle itself is UI-only. |
| "Preserve this repository" (Arctic Code Vault) | No API | |
| Restrict wiki editing to collaborators | No API | The wiki itself is toggled by `has_wiki`; this sub-option is not exposed. |
| Include Git LFS objects in archives | No API | |
| Limit branches/tags updated in a single push | No API | |
| Table of contents / code view options | No API | Per-viewer preferences, not repository state. |

**Managed here:** description, homepage, visibility, topics, template
flag, web commit sign-off, the wiki / issues / projects / discussions
toggles, every merge-button and merge-commit-message option, auto-merge,
auto-delete head branches, suggest-updating-branches, and archived.

## Settings → Access

| Setting | Reason | Notes |
|---|---|---|
| Pending collaborator invitations | Out of scope | An invitation is a transient state, not desired state. Adding a collaborator creates one; the reconciler does not manage its lifecycle, and a listed collaborator stays "to be created" until they accept. |
| Base permissions for org members | Org-level | Set on the organization; it is the floor under every repo. |
| Outside-collaborator vs org-member distinction | Partly | `collaborators.yml` manages **direct** grants only. Access someone has through org membership or a team is invisible to this lane by design — listing it would make every reconcile try to revoke it. |
| Moderation: interaction limits | Not implemented | `PUT /repos/{o}/{r}/interaction-limits` exists. Temporary by nature (24h–6mo expiry), which sits awkwardly with declarative config. |
| Moderation: code review limits | No API | |
| Moderation: reported content | No API | |

**Managed here:** team access with permission levels, direct
collaborators with permission levels.

## Settings → Branches and rules

| Setting | Reason | Notes |
|---|---|---|
| Branch protection: restrict who can dismiss reviews | Not implemented | The API field is `dismissal_restrictions`; the schema has no equivalent yet. |
| Branch protection: allow specified actors to bypass required PRs | Not implemented | `bypass_pull_request_allowances`. |
| Branch protection: lock branch | Not implemented | `lock_branch`, plus `allow_fork_syncing`. |
| Branch protection: block creations | Not implemented | `block_creations`. |
| Branch protection: require deployments to succeed | Not implemented | `required_deployment_environments`. |
| Branch protection: require merge queue | Not implemented | Merge queue configuration is a separate API surface. |
| Merge queue settings | Not implemented | |
| Tag protection rules | Out of scope | Deprecated by GitHub in favour of rulesets with `target: tag`, which **are** managed here. |
| Organization-level rulesets | Org-level | Repo rulesets are managed; org rulesets that apply to this repo are not, and they can override what you set here. |

**Managed here:** branch protection per pattern (status checks, admin
enforcement, PR review requirements including approval count and
code-owner review, push restrictions, linear history, force-push and
deletion toggles, conversation resolution, signed commits), and rulesets
in full — including rule types this service has never heard of, because
rules are carried as an open `type` plus `parameters` map rather than a
fixed list.

> **Ownership warning.** Listing a branch in `branches.yml` takes
> ownership of that branch's *entire* protection record. GitHub's update
> endpoint is a whole-object replace with no way to leave a sub-setting
> alone, so anything you configure in the UI but omit from YAML is turned
> off on the next reconcile. The dry-run comment shows this explicitly.

## Settings → Actions

| Setting | Reason | Notes |
|---|---|---|
| Artifact and log retention period | Not implemented | Repo-level override of an org default; API exists. |
| Fork PR workflows from outside collaborators | Not implemented | `PUT /repos/{o}/{r}/actions/permissions/access` and related. A meaningful supply-chain control — a good next addition. |
| Fork PR workflows in private repositories | Not implemented | Same endpoint family. |
| Actions access for private repos (sharing) | Not implemented | |
| Self-hosted runners | Out of scope | Runner registration involves short-lived tokens and machine state; it is not repository configuration. |
| Runner groups | Org-level | |
| Required workflows | Org-level | Configured on the organization. |
| Workflow-level settings (`.github/workflows/*.yml`) | File, not setting | Already config-as-code. |

**Managed here:** Actions enabled/disabled, the allowed-actions mode, the
allow-list itself (`github_owned_allowed`, `verified_allowed`,
`patterns_allowed`), and the default `GITHUB_TOKEN` permissions
(`default_workflow_permissions`, `can_approve_pull_request_reviews`).

## Settings → Secrets and variables

| Setting | Reason | Notes |
|---|---|---|
| Actions secret **values** | Out of scope | This service manages which secrets exist, never what they contain. A secret value in a Git-tracked YAML file is a secret you have leaked. Declare the name in `secrets.yml` and set the value in the UI or your secret store; a name with no value yet reports as `pending`, not as a failure. |
| Dependabot secrets | Not implemented | Separate API surface from Actions secrets. |
| Codespaces secrets | Not implemented | |
| Environment secrets | Not implemented | The value problem above applies; name-only management is still possible and is a candidate. |
| Environment variables | Not implemented | No value problem here — these are not secret. A good next addition. |

**Managed here:** repository Actions **variables** (names and values —
they are not secret), and repository Actions **secret names** (creation
of the value is yours; deletion of an undeclared secret is ours).

## Settings → Environments

| Setting | Reason | Notes |
|---|---|---|
| Named deployment branch policies | Not implemented | `custom_branch_policies: true` is applied, but the individual branch/tag patterns need `POST .../deployment-branch-policies`. Set the flag here, add the patterns in the UI. |
| Custom deployment protection rules (third-party apps) | Not implemented | |
| Environment secrets and variables | Not implemented | See above. |

**Managed here:** environment existence, wait timer, required reviewers,
prevent-self-review, and the two deployment-branch-policy mode flags.

## Settings → Security

| Setting | Reason | Notes |
|---|---|---|
| Dependency graph | Not implemented | Enabled by default on public repos; the toggle has an API. |
| Dependabot version updates | File, not setting | `.github/dependabot.yml`. |
| Grouped security updates | Not implemented | |
| Code scanning default setup | Not implemented | CodeQL default setup has an API; it is a heavier operation than a settings toggle. |
| Secret scanning validity checks | Not implemented | `security_and_analysis.secret_scanning_validity_checks`. |
| Secret scanning non-provider patterns | Not implemented | |
| Custom secret scanning patterns | Not implemented | |
| Security policy (`SECURITY.md`) | File, not setting | |
| Code owners (`CODEOWNERS`) | File, not setting | Branch protection's code-owner review requirement **is** managed. |

**Managed here:** Dependabot alerts, Dependabot security updates, secret
scanning, secret scanning push protection, and private vulnerability
reporting — each on its own endpoint.

## Settings → Webhooks, Pages, Integrations

| Setting | Reason | Notes |
|---|---|---|
| Webhook secret | Not implemented | The field `secret_ref` is reserved in the schema but rejected at validation: there is no secret store for this service to resolve a reference against yet. Set the secret in the UI. Everything else about the hook is managed, and updating a hook does not clear a secret set out of band. |
| Webhook SSL certificate / delivery history | No API | Deliveries are observable, not configurable. |
| Pages: "public" site visibility | Not implemented | Applies to private-repo Pages on Enterprise plans. |
| Pages: custom domain verification | No API | Verification is a DNS-plus-UI flow. |
| GitHub Apps installed on the repo | Out of scope | An app installation is a grant of authority to a third party. It should not be silently altered by merging a settings PR. |
| Notification / email routing settings | No API | |

**Managed here:** webhooks (URL, content type, insecure-SSL flag, active
flag, event list), Pages build type, source branch and path, CNAME, and
HTTPS enforcement.

## Not repository settings at all

These come up often enough to be worth stating explicitly.

| Thing | Where it lives |
|---|---|
| Issue labels and milestones | No API-backed declarative support here. `safe-settings` manages labels; this service does not, and adding it is a reasonable request. |
| Discussion categories | UI only. |
| Projects (boards), their fields and views | A separate product surface with its own API. |
| Organization members, teams, and team membership | Org-level. This service binds **existing** teams to repositories; it does not create teams or manage who is in them. |
| Organization custom property **definitions** | Org-level. This service sets property **values** on a repository; the property must already be defined on the org. |
| Organization webhooks, secrets, and Actions policy | Org-level. |
| Enterprise policies | Enterprise-level, and they override everything below them. |

## When a UI setting and this service disagree

The reconciler is one-sided by design for scalar fields: a setting you
never mention in YAML is left exactly as it is. Two exceptions are worth
knowing, because both can delete work you did in the UI:

1. **Collections are authoritative.** A webhook, autolink, deploy key,
   variable, secret, team binding, collaborator, environment or ruleset
   that exists on GitHub but is absent from your YAML is **deleted**.
   Adding one in the UI and forgetting to add it to YAML means losing it
   on the next reconcile. A per-section `prune` opt-out is planned but
   not shipped.

2. **Branch protection is a whole-object replace**, as described above.

Everything else on this page is inert to the reconciler. If you need a
setting managed that this page lists as *Not implemented*, that is a
feature request with a known API behind it — the fastest ones to add are
`default_branch`, `allow_forking`, environment variables, and the
fork-PR-workflow controls.

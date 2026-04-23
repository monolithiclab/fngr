# Publishing playbook

Operational reference for shipping any monolithiclab Go CLI through the
same multi-channel release pipeline that fngr uses: GitHub Releases
(cross-compiled binaries + cosign-signed SHA256SUMS) + multi-arch
container image on `ghcr.io` + Homebrew formula on
`monolithiclab/homebrew-tap`. This is the playbook distilled from
shipping fngr v0.0.1 — every step, every secret, every gotcha.

The fngr files (`Dockerfile`, `.goreleaser.yaml`,
`.github/workflows/{ci,release}.yml`, `LICENSE`) are the canonical
templates — copy them into the new repo and search-replace `fngr`
with the new project name.

---

## Prerequisites

On your local machine:

- `gh` CLI authenticated as a user with admin rights on the
  `monolithiclab` org. Required scopes for the playbook to run end-to-
  end: `repo, admin:org, read:packages, write:packages`. Refresh on
  demand with `gh auth refresh -h github.com -s <scope1>,<scope2>`.
- Go (matching what your project's `go.mod` declares).
- Docker with buildx.
- GoReleaser (`brew install goreleaser`) — for local config validation
  and snapshot rehearsals before tagging.
- `cosign` (`brew install cosign`) — for verifying release artifacts.

---

## Org-level one-time setup

Skip this whole section if it's already done for the
`monolithiclab` org (it's done as of fngr v0.0.1). Re-read it only
if you're spinning up a new org or troubleshooting policy issues.

### 1. Allow public container packages

The org's package-creation policy must permit public visibility for
container packages, otherwise the per-package "Change visibility"
dialog returns "disallowed by org administrators".

UI-only setting (no REST API surfaces it):

1. Open `https://github.com/organizations/monolithiclab/settings/packages`.
2. Find the **Container packages** section (label varies by GitHub
   UI revision — look for "Allowed visibilities" or "Public packages").
3. Ensure **Public** is checked.
4. Save.

### 2. Create the shared Homebrew tap repo

One tap repo per org, shared across all CLIs the org publishes
formulas for. fngr uses `monolithiclab/homebrew-tap`.

```bash
gh repo create monolithiclab/homebrew-tap --public \
  --description "Homebrew tap for monolithiclab CLIs" \
  --add-readme
```

### 3. Mint the Homebrew tap PAT (one PAT, reused across projects)

Browser-only — `gh` CLI doesn't yet support fine-grained PAT creation.

1. Open `https://github.com/settings/personal-access-tokens/new`
   (the personal account you want to own the PAT — usually the org
   owner).
2. Token name: `monolithiclab-homebrew-tap-token` (or per-project if
   you want strict separation).
3. Expiration: 1 year — set a calendar reminder for rotation.
4. **Resource owner**: `monolithiclab` (NOT your personal account —
   the PAT must target the org).
5. **Repository access**: "Only select repositories" → choose
   `monolithiclab/homebrew-tap`. (Or "All repositories" — `homebrew-tap`
   is the only repo it'll touch in practice; the narrower scope is
   safer.)
6. **Repository permissions**:
   - **Contents**: Read and write  ← critical, the GoReleaser brew
     step PUTs `/repos/.../contents/<formula>.rb`. Read-only here is
     the most common footgun and produces an opaque
     `403 Resource not accessible by personal access token` from the
     workflow.
   - **Metadata**: Read-only (auto-included).
   - Everything else: No access.
7. **Account permissions**: none.
8. Click **Generate token** and copy it immediately (single-view).

> **Verification before storing it**: confirm the PAT can WRITE, not
> just read — the GitHub UI's "Read and write" dropdown is easy to
> mis-set or leave un-saved (no obvious save indicator). Run:
>
> ```bash
> curl -sI -X PUT \
>   -H "Authorization: Bearer <PAT>" \
>   -H "Accept: application/vnd.github+json" \
>   -d '{"message":"perm test","content":"dGVzdA=="}' \
>   https://api.github.com/repos/monolithiclab/homebrew-tap/contents/.perm-test \
>   | head -1
> ```
>
> Expect `HTTP/2 201`. A `403` means the PAT lacks Contents:Write —
> re-edit the PAT, double-check the dropdown, scroll to the bottom
> of the page, click **Update**. Then clean up the test file.

---

## Per-repo setup (the actual playbook)

For each new repo that wants to publish via this pipeline.

### 1. Add the file scaffolding

Copy these files from fngr verbatim (search-replace `fngr` with the
new project name):

- `LICENSE` — MIT (or pick another SPDX; update `.goreleaser.yaml`'s
  `brews[0].license` to match).
- `Dockerfile` — distroless-static-debian13 base, single COPY of the
  binary. Adjust the binary name in the COPY + ENTRYPOINT lines.
- `.goreleaser.yaml` — the load-bearing release config. Search-
  replace `fngr` and `monolithiclab/fngr` and `homebrew-tap`.
- `.github/workflows/ci.yml` — push-to-main + PR matrix on
  ubuntu+macOS, `make lint test`, coverage artifact.
- `.github/workflows/release.yml` — tag-triggered, QEMU + Buildx
  + ghcr.io login + cosign-installer + goreleaser-action.

### 2. Wire the `HOMEBREW_TAP_TOKEN` secret as REPO-level

> **Do NOT use `gh secret set --org ... --visibility selected`** —
> see "Gotchas" below. Despite passing all visibility checks, the
> resulting secret reaches the workflow runner as an empty string,
> causing `goreleaser` to fall back to `GITHUB_TOKEN` (which can't
> write to other repos) and 401 on every brew step.

```bash
printf '%s' '<paste-PAT-here>' \
  | gh secret set HOMEBREW_TAP_TOKEN --repo monolithiclab/<new-repo>
```

Verify it's stored:

```bash
gh secret list --repo monolithiclab/<new-repo>
```

You should see `HOMEBREW_TAP_TOKEN` with a recent `Updated` timestamp.

### 3. Make the repo public

Required for branch protection on the free org plan, and for the
ghcr.io image to be pull-able without auth.

```bash
gh repo edit monolithiclab/<new-repo> --visibility public --accept-visibility-change-consequences
```

### 4. Land the workflows on `main`

Push the `.github/workflows/{ci,release}.yml` commit to `main`. The
CI workflow fires immediately; verify it passes:

```bash
gh run watch --repo monolithiclab/<new-repo>
gh run view <run-id> --repo monolithiclab/<new-repo> --json conclusion --jq .conclusion
```

> **Watcher gotcha**: `gh run watch --exit-status` returns 0 when the
> watcher itself completes successfully — it does NOT track whether
> the run succeeded or failed. Always re-check `--json conclusion`
> after the notification.

### 5. Local rehearsal (optional but recommended)

Before tagging the first release:

```bash
goreleaser check                                          # validates config; deprecation warnings on dockers/brews are intentional (see Gotchas)
goreleaser release --snapshot --skip=publish,sign --clean # full local build
ls dist/                                                  # 4 archives, SHA256SUMS, dist/homebrew/<name>.rb, multi-arch images
rm -rf dist/
```

### 6. Cut the first release — pre-release flow

Always tag `v0.0.1-rc1` first to validate the pipeline before any
stable tag. Pre-release tags skip the `:latest` Docker tag and the
brew formula bump (via `skip_push: auto` / `skip_upload: auto`),
so you can iterate without polluting stable channels.

```bash
git tag -a v0.0.1-rc1 -m "Release v0.0.1-rc1: pipeline validation"
git push origin v0.0.1-rc1
gh run watch --repo monolithiclab/<new-repo>
```

Verify on success:

```bash
# GitHub Release marked pre-release with full asset set
gh release view v0.0.1-rc1 --repo monolithiclab/<new-repo> \
  --json isPrerelease,assets --jq '{isPrerelease, assets:[.assets[].name]}'

# cosign verify the SHA256SUMS file
gh release download v0.0.1-rc1 --repo monolithiclab/<new-repo> --pattern 'SHA256SUMS*' --dir /tmp/verify
cosign verify-blob \
  --signature /tmp/verify/SHA256SUMS.sig \
  --certificate /tmp/verify/SHA256SUMS.pem \
  --certificate-identity-regexp 'https://github.com/monolithiclab/<new-repo>' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  /tmp/verify/SHA256SUMS

# Multi-arch container manifest exists at the rc1 tag
gh auth token | docker login ghcr.io -u "$(gh api user --jq .login)" --password-stdin
docker buildx imagetools inspect ghcr.io/monolithiclab/<new-repo>:0.0.1-rc1

# :latest was NOT bumped (correct for pre-release)
docker manifest inspect ghcr.io/monolithiclab/<new-repo>:latest 2>&1 | head -1  # expect "manifest unknown"

# Homebrew tap NOT touched (correct for pre-release)
gh api /repos/monolithiclab/homebrew-tap/contents 2>&1 | grep '<new-repo>.rb' || echo "no formula yet (correct)"
```

### 7. Cut the stable release

```bash
git tag -a v0.0.1 -m "Release v0.0.1: initial public release"
git push origin v0.0.1
gh run watch --repo monolithiclab/<new-repo>
```

Verify the stable release adds `:latest` and the brew formula:

```bash
docker manifest inspect ghcr.io/monolithiclab/<new-repo>:latest
gh api /repos/monolithiclab/homebrew-tap/contents/<new-repo>.rb --jq .name
```

(Note: GoReleaser writes the formula at the **root** of the tap repo
by default — `<new-repo>.rb`, not `Formula/<new-repo>.rb`. Both
layouts work for `brew install monolithiclab/tap/<new-repo>`. To
prefer `Formula/`, add `directory: Formula` to the `brews:` block.)

### 8. Post-first-release setup

Two UI-only steps GitHub doesn't expose via REST:

**Flip the ghcr.io package visibility to public.** First push of any
ghcr.io package creates it as private. Until you flip:

1. Browse to `https://github.com/orgs/monolithiclab/packages/container/<new-repo>/settings`
2. Scroll to **Danger Zone**.
3. **Change visibility** → **Public** → type the package name to
   confirm → **I understand the consequences, change package visibility**.
4. Verify with an unauthenticated pull from another machine (or
   `docker logout ghcr.io && docker pull ghcr.io/monolithiclab/<new-repo>:0.0.1`).

**Enable branch protection on `main` requiring CI green.** Free
plan supports branch protection only on public repos.

```bash
gh api -X PUT /repos/monolithiclab/<new-repo>/branches/main/protection \
  --field 'required_status_checks[strict]=true' \
  --field 'required_status_checks[contexts][]=test (ubuntu-latest)' \
  --field 'required_status_checks[contexts][]=test (macos-latest)' \
  --field 'enforce_admins=false' \
  --field 'required_pull_request_reviews=null' \
  --field 'restrictions=null'
```

(Use `--field`, not `--raw-field` — the API needs proper boolean/null
types and `--raw-field` sends strings. The 422 it returns lists every
schema mismatch.)

---

## Verifying a published release as a downstream user

```bash
# Homebrew
brew install monolithiclab/tap/<name>
<name> --version

# Docker
docker run --rm ghcr.io/monolithiclab/<name>:0.0.1 --version

# Tarball + cosign
gh release download v0.0.1 --repo monolithiclab/<name> --pattern 'SHA256SUMS*' --dir /tmp/v
gh release download v0.0.1 --repo monolithiclab/<name> --pattern '*_linux_amd64.tar.gz' --dir /tmp/v
cosign verify-blob \
  --signature /tmp/v/SHA256SUMS.sig --certificate /tmp/v/SHA256SUMS.pem \
  --certificate-identity-regexp 'https://github.com/monolithiclab/<name>' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  /tmp/v/SHA256SUMS
(cd /tmp/v && sha256sum -c SHA256SUMS 2>/dev/null | grep linux_amd64)
```

---

## Gotchas (lessons learned shipping fngr v0.0.1)

Each entry is a real failure mode we hit and the fix that worked.
Read them before reusing this playbook on a new repo.

### Org-level Actions secrets with `--visibility selected` arrived as empty strings in the workflow

Setting `HOMEBREW_TAP_TOKEN` via
`gh secret set --org monolithiclab --visibility selected --repos <repo>`
created a secret that:

- Showed up in `gh secret list --org monolithiclab` correctly.
- Reported `monolithiclab/<repo>` in
  `/orgs/monolithiclab/actions/secrets/HOMEBREW_TAP_TOKEN/repositories`.
- Was reachable via `secrets.HOMEBREW_TAP_TOKEN` in the workflow yaml.
- ...but the env var **was empty** at runtime. (Confirmed via a
  one-shot debug step that printed the token's length with mask.)

GoReleaser then silently fell back to `GITHUB_TOKEN`, which can read
the workflow's own repo but not `homebrew-tap`, producing the
opaque `401 Bad credentials` on the brew step.

**Workaround**: set the secret as a **repo-level** secret instead.
Repo-level secrets take precedence over same-named org secrets and
worked first try.

```bash
printf '%s' '<PAT>' | gh secret set HOMEBREW_TAP_TOKEN --repo monolithiclab/<new-repo>
```

If you find the root cause of the org-level breakage and fix it,
update this playbook.

### `cosign-installer@v4` broke our signing args

Cosign v4 defaults to `--new-bundle-format`, which:

- Deprecates `--output-signature` and `--output-certificate` (the
  flags `.goreleaser.yaml`'s `signs:` config passes).
- Produces a single `.sigstore.json` bundle instead of the
  `.sig` + `.pem` pair we publish on each release and reference in
  the README's verification example.

Pin `sigstore/cosign-installer@v3` until you migrate the
`signs:` config to the new bundle shape and update the README's
`cosign verify-blob` command. (Note: `sigstore/cosign-installer`
doesn't ship a moving `v4` major-alias tag — only specific patch
tags like `v4.1.1`. If you do migrate, pin the patch.)

### `dockers:` and `brews:` are deprecated by GoReleaser, but kept intentionally

`goreleaser check` warns about both on every run. We're keeping them:

- **`dockers_v2:`** needs a multi-stage Dockerfile that uses buildx's
  `TARGETOS`/`TARGETARCH` build args to pick the right per-platform
  binary. Non-trivial Dockerfile rewrite.
- **`homebrew_casks:`** generates Homebrew **Cask** DSL files (under
  `Casks/`), which are macOS-only and require `brew install --cask`.
  The CLIs we publish are Linux+macOS, so we need
  Formula generation, which `brews:` still does. Revisit when
  GoReleaser ships a `homebrew_formulas:` key.

### Re-running a partially-failed release fails on `422 already_exists`

If the release workflow fails after creating the GitHub Release but
before the brew step completes, re-running the workflow tries to
re-upload the binary assets and 422s on every one.

`.goreleaser.yaml` has `release.replace_existing_artifacts: true` to
make re-runs idempotent on the asset-upload phase. If you forget to
set this and hit the issue, `gh release delete <tag> --cleanup-tag=false`
the partial release and rerun — assets will be re-uploaded byte-
identical (same builds, same checksums).

### Fine-grained PATs: trust the API, not the UI

Editing a PAT's permissions in the GitHub UI shows changes
immediately, but **propagation to actual API enforcement can lag a
few seconds to a few minutes**. We hit a case where:

- The UI showed `Read and Write access to code` clearly.
- A direct PUT to `/contents/...` returned 403 for ~2 minutes.
- A retry minutes later returned 201.

The authoritative test is the `x-accepted-github-permissions`
response header on a write attempt:

```bash
curl -sI -X PUT \
  -H "Authorization: Bearer <PAT>" \
  -H "Accept: application/vnd.github+json" \
  -d '{"message":"x","content":"eA=="}' \
  https://api.github.com/repos/<owner>/<repo>/contents/.test \
  | grep -i 'x-accepted-github-permissions'
```

If the response is 403 and the header lists
`contents=write` as an accepted scope, the PAT lacks write. If the
response is 201, the PAT works (then `gh api -X DELETE` to clean up
the test file).

### Container package visibility is UI-only

There is no stable REST API to flip a ghcr.io package between
private and public. Every API path (`PATCH /orgs/.../packages/...`,
the `gh package` subcommand) returns 404. Use the GitHub UI per
section "Post-first-release setup" above.

### `gh secret set` reads stdin when `--body` is omitted

These two are equivalent:

```bash
printf '%s' '<value>' | gh secret set NAME --repo <r>
gh secret set NAME --repo <r> --body '<value>'
```

There is no `--body-file` flag (one early attempt to use it failed
silently and left the prior secret value in place — verify the
`Updated` timestamp moved after every set).

### `gh run watch --exit-status` lies

It returns 0 when the watcher itself exits cleanly, even if the run
it was watching failed. After every notification:

```bash
gh run view <id> --repo <r> --json conclusion --jq .conclusion
```

### Branch protection PUT needs typed fields

`gh api -X PUT .../branches/main/protection --raw-field strict=true`
fails with a confusing 422 because `--raw-field` sends string
`"true"`, not boolean `true`. Use `--field`, which auto-coerces
booleans, integers, and `null` from the literal strings.

### Branch protection on free plan requires public repo

Free orgs/users can only set branch protection on public repos.
Private repos return `403 Upgrade to GitHub Pro or make this
repository public to enable this feature`. For an open-source CLI
distributed via Homebrew + ghcr.io, the source repo being public
is the conventional posture anyway.

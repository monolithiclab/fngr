# GitHub Actions CI + Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship two GitHub Actions workflows (CI on push/PR, release on `v*.*.*` tag) plus a GoReleaser-driven multi-channel release pipeline (GitHub Releases + ghcr.io multi-arch container + Homebrew tap), with cosign keyless signing across the board.

**Architecture:** Two workflow files at `.github/workflows/`, one declarative `.goreleaser.yaml` at repo root, one minimal `Dockerfile` consumed by GoReleaser's `dockers:` section. No production code changes. License decision: MIT.

**Tech Stack:** GitHub Actions, GoReleaser v2, cosign (Sigstore keyless), Docker Buildx + QEMU (multi-arch), distroless static-debian13 (container base), Homebrew tap pattern.

**Spec:** `docs/superpowers/specs/2026-04-22-github-actions-design.md`

**File map:**
- Create: `LICENSE` (MIT, repo root) — required by GoReleaser archives + brew formula
- Create: `Dockerfile` (repo root) — distroless base, COPY binary, ENTRYPOINT
- Create: `.goreleaser.yaml` (repo root) — full release config
- Create: `.github/workflows/ci.yml` — CI workflow
- Create: `.github/workflows/release.yml` — Release workflow
- Modify: `README.md` — add "Container usage" subsection + brew install line + ghcr install line
- Modify: `docs/superpowers/roadmap.md` — move CI + release items to Done

**External one-time setup** (Task 5 below): create `monolithiclab/homebrew-tap` repo, mint a fine-grained PAT, store as org-level secret with selected-repo visibility.

---

## Task 1: Add MIT `LICENSE`

GoReleaser's `archives.files: [LICENSE, README.md]` reads this file; missing `LICENSE` would fail every release. Tiny first task — unblocks everything else.

**Files:**
- Create: `LICENSE`

- [ ] **Step 1.1: Write the LICENSE file**

```
MIT License

Copyright (c) 2026 Nicolas Mussat

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 1.2: Verify license is valid SPDX**

Run: `head -1 LICENSE`
Expected: `MIT License`

- [ ] **Step 1.3: Commit**

```bash
git add LICENSE
git commit -m "$(cat <<'EOF'
docs: add MIT LICENSE

Required by upcoming GoReleaser pipeline (archives.files references
LICENSE; brew formula declares license: 'MIT'). Spec at
docs/superpowers/specs/2026-04-22-github-actions-design.md.
EOF
)"
```

---

## Task 2: Add `Dockerfile`

Minimal distroless base; the binary is cross-compiled by GoReleaser and copied in. Standalone file — testing comes in Task 3 when GoReleaser actually consumes it.

**Files:**
- Create: `Dockerfile`

- [ ] **Step 2.1: Write the Dockerfile**

```dockerfile
# Image is built by GoReleaser. The binary is cross-compiled outside this
# Dockerfile and made available in the build context as `fngr`.
FROM gcr.io/distroless/static-debian13

COPY fngr /fngr

ENTRYPOINT ["/fngr"]
```

- [ ] **Step 2.2: Lint the Dockerfile** (optional, only if `hadolint` is installed)

Run: `which hadolint && hadolint Dockerfile || echo "hadolint not installed; skipping"`
Expected: PASS or "skipping"

- [ ] **Step 2.3: Commit**

```bash
git add Dockerfile
git commit -m "$(cat <<'EOF'
build: add distroless Dockerfile

Consumed by GoReleaser's dockers: section in the upcoming release
pipeline. distroless/static-debian13 base provides CA certs +
/usr/share/zoneinfo + /tmp at ~2 MB; the cross-compiled fngr binary
copies in via the build context.
EOF
)"
```

---

## Task 3: Add `.goreleaser.yaml` and validate locally

The full release configuration. Validate end-to-end with a snapshot build before any workflow ever runs.

**Files:**
- Create: `.goreleaser.yaml`

- [ ] **Step 3.1: Write `.goreleaser.yaml`**

```yaml
version: 2

project_name: fngr

before:
  hooks:
    - go mod tidy

builds:
  - id: fngr
    main: ./cmd/fngr
    binary: fngr
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}

archives:
  - id: fngr
    builds: [fngr]
    name_template: '{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}'
    formats: [tar.gz]
    files: [LICENSE, README.md]

checksum:
  name_template: 'SHA256SUMS'

signs:
  - id: cosign-checksums
    cmd: cosign
    signature: '${artifact}.sig'
    certificate: '${artifact}.pem'
    args:
      - sign-blob
      - '--output-certificate=${certificate}'
      - '--output-signature=${signature}'
      - '${artifact}'
      - '--yes'
    artifacts: checksum

dockers:
  - id: fngr-amd64
    image_templates: ['ghcr.io/monolithiclab/fngr:{{ .Version }}-amd64']
    use: buildx
    dockerfile: Dockerfile
    build_flag_templates:
      - '--platform=linux/amd64'
      - '--label=org.opencontainers.image.source=https://github.com/monolithiclab/fngr'
      - '--label=org.opencontainers.image.version={{ .Version }}'
      - '--label=org.opencontainers.image.revision={{ .FullCommit }}'
  - id: fngr-arm64
    image_templates: ['ghcr.io/monolithiclab/fngr:{{ .Version }}-arm64']
    use: buildx
    dockerfile: Dockerfile
    goarch: arm64
    build_flag_templates:
      - '--platform=linux/arm64'
      - '--label=org.opencontainers.image.source=https://github.com/monolithiclab/fngr'
      - '--label=org.opencontainers.image.version={{ .Version }}'
      - '--label=org.opencontainers.image.revision={{ .FullCommit }}'

docker_manifests:
  - name_template: 'ghcr.io/monolithiclab/fngr:{{ .Version }}'
    image_templates:
      - 'ghcr.io/monolithiclab/fngr:{{ .Version }}-amd64'
      - 'ghcr.io/monolithiclab/fngr:{{ .Version }}-arm64'
  - name_template: 'ghcr.io/monolithiclab/fngr:latest'
    skip_push: auto
    image_templates:
      - 'ghcr.io/monolithiclab/fngr:{{ .Version }}-amd64'
      - 'ghcr.io/monolithiclab/fngr:{{ .Version }}-arm64'

docker_signs:
  - cmd: cosign
    args: ['sign', '--yes', '${artifact}@${digest}']
    artifacts: manifests

brews:
  - name: fngr
    repository:
      owner: monolithiclab
      name: homebrew-tap
      token: '{{ .Env.HOMEBREW_TAP_TOKEN }}'
    homepage: 'https://github.com/monolithiclab/fngr'
    description: 'Command-line journal for logging events with FTS, tags, and trees.'
    license: 'MIT'
    skip_upload: auto
    install: |
      bin.install "fngr"
    test: |
      assert_match "fngr", shell_output("#{bin}/fngr --version")

changelog:
  use: git
  sort: asc
  filters:
    exclude:
      - '^Merge'
      - '^chore'
  groups:
    - title: 'Features'
      regexp: '^.*?feat(\(.+\))?:'
      order: 0
    - title: 'Bug fixes'
      regexp: '^.*?fix(\(.+\))?:'
      order: 1
    - title: 'Documentation'
      regexp: '^.*?docs(\(.+\))?:'
      order: 2
    - title: 'Other'
      order: 999

release:
  github:
    owner: monolithiclab
    name: fngr
  prerelease: auto
```

- [ ] **Step 3.2: Install GoReleaser locally** (if not already)

Run: `which goreleaser || brew install goreleaser`
Expected: a binary path printed (or installer output)

- [ ] **Step 3.3: Validate config syntax**

Run: `goreleaser check`
Expected: `1 configuration file(s) validated` (no errors)

- [ ] **Step 3.4: Run a snapshot release locally**

Run: `goreleaser release --snapshot --skip=publish,sign --clean`
Expected: completes without errors. `dist/` should contain:
- `fngr_<snapshot>_linux_amd64.tar.gz`, `..._linux_arm64.tar.gz`, `..._darwin_amd64.tar.gz`, `..._darwin_arm64.tar.gz`
- `SHA256SUMS`
- `fngr_<snapshot>_linux_amd64/fngr` (the raw binary)
- `metadata.json`, `artifacts.json`, `config.yaml`

Verify each archive contains the binary, LICENSE, and README:
Run: `tar tzf dist/fngr_*_linux_amd64.tar.gz`
Expected: lists `fngr`, `LICENSE`, `README.md`

- [ ] **Step 3.5: Verify cross-compiled binary works**

Run: `./dist/fngr_linux_amd64_v1/fngr --version 2>/dev/null || ./dist/fngr_linux_amd64/fngr --version`
(GoReleaser's binary path varies by version; pick whichever exists.)
Expected: a version string (snapshot builds emit the snapshot tag, e.g., `0.0.0-next-<sha>`)

- [ ] **Step 3.6: Verify Docker image builds locally**

Run: `goreleaser release --snapshot --skip=publish,sign --clean 2>&1 | grep -E 'building docker|cmd: docker'`
Expected: lines showing `docker build` invocations for amd64 and arm64.

If you want to test the actual Docker run:
Run: `docker run --rm ghcr.io/monolithiclab/fngr:<snapshot-tag>-amd64 --version`
Expected: version string. (The image was built locally; tag visible via `docker images | grep fngr`.)

- [ ] **Step 3.7: Clean up snapshot artifacts**

Run: `rm -rf dist/`

- [ ] **Step 3.8: Commit**

```bash
git add .goreleaser.yaml
git commit -m "$(cat <<'EOF'
build: add .goreleaser.yaml

Single source of truth for release pipeline:
- 4 OS/arch binaries (linux/darwin × amd64/arm64), CGO_ENABLED=0,
  -s -w stripped, version injected via -ldflags
- tar.gz archives bundling LICENSE + README
- SHA256SUMS checksums file
- cosign keyless signing (checksums + container manifests)
- Multi-arch ghcr.io image via buildx (amd64 + arm64 manifests)
- Homebrew formula on monolithiclab/homebrew-tap
- Conventional-Commits-grouped changelog (feat/fix/docs/other)
- Pre-release auto-detection on -rc/-beta/-alpha tags

Validated locally with `goreleaser check` and a snapshot build.
EOF
)"
```

---

## Task 4: Add `.github/workflows/ci.yml`

CI workflow runs on every push to main and every PR. The first time it runs is when this commit lands on main.

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 4.1: Create the workflow directory**

Run: `mkdir -p .github/workflows`
Expected: directory created (or already exists)

- [ ] **Step 4.2: Write `ci.yml`**

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: make lint test
      - if: matrix.os == 'ubuntu-latest'
        uses: actions/upload-artifact@v4
        with:
          name: coverage
          path: cover.out
          if-no-files-found: error
          retention-days: 14
```

- [ ] **Step 4.3: Lint the YAML** (optional)

Run: `which yamllint && yamllint .github/workflows/ci.yml || echo "yamllint not installed; skipping"`
Expected: PASS or "skipping"

- [ ] **Step 4.4: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "$(cat <<'EOF'
ci: add CI workflow

Runs on push to main + every PR. Matrix on ubuntu-latest +
macos-latest, both running `make lint test`. Coverage profile
uploaded as artifact from the ubuntu cell only (avoids same-name
overwrites). Concurrency block kills in-flight CI on force-push.
go-version-file binds the toolchain to the project's go.mod.
EOF
)"
```

- [ ] **Step 4.5: After this commit lands on main, verify CI runs**

Push the commit (or merge if going through a PR):
Run: `git push origin main`

Then watch:
Run: `gh run watch --repo monolithiclab/fngr`
Expected: the CI workflow runs on both matrix cells; both pass; the coverage artifact is uploaded.

If anything fails, investigate before proceeding to Task 5.

---

## Task 5: One-time external setup (tap repo + PAT + secret)

These are commands executed against GitHub itself, not file changes. No commit at the end. The task gates on Task 6 (release workflow needs the secret to exist).

**Prerequisites:**
- `gh` CLI authenticated as a user with admin rights on `monolithiclab` org
- Browser access for the PAT creation step

- [ ] **Step 5.1: Create the Homebrew tap repository**

Run:
```bash
gh repo create monolithiclab/homebrew-tap --public \
  --description "Homebrew tap for monolithiclab CLIs" \
  --add-readme
```
Expected: `https://github.com/monolithiclab/homebrew-tap` URL printed.

Verify:
Run: `gh repo view monolithiclab/homebrew-tap`
Expected: repo metadata shown with public visibility.

- [ ] **Step 5.2: Create a fine-grained Personal Access Token**

This step is browser-based; the `gh` CLI doesn't yet support fine-grained PAT creation.

1. Open `https://github.com/settings/personal-access-tokens/new`
2. Token name: `fngr-release-homebrew-tap-token`
3. Expiration: 1 year from today
4. Resource owner: `monolithiclab`
5. Repository access: "Only select repositories" → `monolithiclab/homebrew-tap`
6. Permissions:
   - Repository permissions: `Contents: Read and write`, `Metadata: Read-only`
   - Account permissions: none
7. Click "Generate token" and **copy it immediately** (single-view).

Set a calendar reminder for 11 months from now to rotate.

- [ ] **Step 5.3: Store the PAT as an org-level secret**

Run:
```bash
gh secret set HOMEBREW_TAP_TOKEN \
  --org monolithiclab \
  --visibility selected \
  --repos fngr
```
You'll be prompted to paste the token. Paste from Step 5.2 and press Enter.

Verify:
Run: `gh secret list --org monolithiclab`
Expected: `HOMEBREW_TAP_TOKEN` appears with `Selected repositories: 1`.

- [ ] **Step 5.4: Sanity-check from the fngr repo**

Run: `gh secret list --repo monolithiclab/fngr`
Expected: `HOMEBREW_TAP_TOKEN` appears (inherited from org).

No commit for this task — external resources only.

---

## Task 6: Add `.github/workflows/release.yml`

Triggered on `v*.*.*` and `v*.*.*-*` tag pushes. Runs the full GoReleaser pipeline. This task lands the file but doesn't trigger anything — Task 8 pushes the first tag.

**Files:**
- Create: `.github/workflows/release.yml`

- [ ] **Step 6.1: Write `release.yml`**

```yaml
name: Release

on:
  push:
    tags:
      - 'v*.*.*'
      - 'v*.*.*-*'

permissions:
  contents: write       # GitHub Release create/upload
  packages: write       # ghcr.io push
  id-token: write       # cosign keyless via OIDC

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0  # GoReleaser needs full history for changelog
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: sigstore/cosign-installer@v3
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
```

- [ ] **Step 6.2: Lint the YAML** (optional)

Run: `which yamllint && yamllint .github/workflows/release.yml || echo "yamllint not installed; skipping"`
Expected: PASS or "skipping"

- [ ] **Step 6.3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "$(cat <<'EOF'
ci: add release workflow

Triggered on v*.*.* and v*.*.*-* tag pushes. Permissions scoped
at workflow level (contents/packages/id-token). QEMU + Buildx
enable multi-arch Docker builds from a single ubuntu runner.
Cosign installer puts cosign on PATH for keyless signing of both
the SHA256SUMS file and the multi-arch container manifests.
HOMEBREW_TAP_TOKEN comes from the org-level secret set up in
the prior task.
EOF
)"
```

---

## Task 7: Documentation updates

Two docs to update: `README.md` (new "Container usage" subsection plus brew + ghcr install lines) and `docs/superpowers/roadmap.md` (move both items to Done).

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/roadmap.md`

- [ ] **Step 7.1: Read the current README "Install" section**

Run: `sed -n '9,21p' README.md`

You should see the existing `go install` line and the `make build` / `make install` block.

- [ ] **Step 7.2: Update README "Install" section + add "Container usage"**

Replace the `## Install` block (lines roughly 9–21) with:

```markdown
## Install

```
go install github.com/monolithiclab/fngr/cmd/fngr@latest
```

Or via Homebrew (macOS / Linux):

```
brew install monolithiclab/tap/fngr
```

Or build from source:

```
make build        # binary at build/fngr
make install      # installs to $GOBIN
```

### Container usage

A multi-arch container image is published on each release at
`ghcr.io/monolithiclab/fngr:<version>` (and `:latest` for stable
releases). The image is ~6 MB, distroless-based.

```
docker run --rm \
  -v "$HOME/.fngr.db:/data/fngr.db" \
  -e FNGR_DB=/data/fngr.db \
  -e TZ=America/New_York \
  ghcr.io/monolithiclab/fngr:latest -S '#ops'
```

Caveats: the container is for scripted use (`add "note"`, `--format=json`,
etc.). Interactive features are disabled — `fngr add -e` needs `$EDITOR`
and a binary in the image (not provided), and the pager needs `less` +
a TTY (also absent). Time-of-day rendering defaults to UTC unless `TZ`
is passed.
```

(The exact `## Install` heading already exists; replace from `## Install` through the closing ` ``` ` of the build-from-source block.)

- [ ] **Step 7.3: Update `docs/superpowers/roadmap.md`** — move both items to Done.

Read the roadmap first:
Run: `cat docs/superpowers/roadmap.md`

Find the `## Project infrastructure` section. Remove it entirely (it becomes empty).

In the `## Done` section, append after the existing last bullet (Markdown output):

```markdown
- **GitHub Actions CI + release pipeline** — every push to `main` and
  every PR validates against `make lint test` on a Linux + macOS
  matrix; every `v*.*.*` tag triggers a GoReleaser-driven multi-channel
  release (GitHub Release with cross-compiled binaries + cosign-signed
  SHA256SUMS, multi-arch container image on `ghcr.io/monolithiclab/fngr`,
  Homebrew formula on `monolithiclab/homebrew-tap`). Pre-release tags
  (`v*.*.*-rc1` etc.) skip the `:latest` Docker tag and the brew
  formula bump.
```

- [ ] **Step 7.4: Commit**

```bash
git add README.md docs/superpowers/roadmap.md
git commit -m "$(cat <<'EOF'
docs: README + roadmap for CI/release pipeline

README "Install" section gains a Homebrew install line and a new
"Container usage" subsection documenting the ghcr.io image, typical
docker run invocation with volume + TZ env, and the
scripted-use-only caveat (no $EDITOR, no pager).

Roadmap consolidates both Project infrastructure items into one
Done bullet; the section is now empty so it's removed.
EOF
)"
```

---

## Task 8: First-release validation (rc1 then stable)

End-to-end test of the pipeline. Push a pre-release tag first, verify everything, then push the stable tag.

**No code changes — pure validation.**

- [ ] **Step 8.1: Verify CI is green on `main` before tagging**

Run: `gh run list --repo monolithiclab/fngr --workflow=CI --branch=main --limit=1`
Expected: latest run shows ✓.

- [ ] **Step 8.2: Push the pre-release tag**

Run:
```bash
git tag v0.0.1-rc1
git push origin v0.0.1-rc1
```

- [ ] **Step 8.3: Watch the release workflow**

Run: `gh run watch --repo monolithiclab/fngr`
Expected: the Release workflow starts, runs goreleaser, takes ~5–10 minutes.

If it fails, investigate logs:
Run: `gh run view --log-failed --repo monolithiclab/fngr`

- [ ] **Step 8.4: Verify the GitHub Release**

Run: `gh release view v0.0.1-rc1 --repo monolithiclab/fngr`
Expected:
- Marked as **pre-release**
- Assets: 4 `.tar.gz` archives, `SHA256SUMS`, `SHA256SUMS.sig`, `SHA256SUMS.pem`
- Body: changelog grouped under Features / Bug fixes / Documentation / Other

- [ ] **Step 8.5: Verify the multi-arch container image**

Run:
```bash
gh auth token | docker login ghcr.io -u $(gh api user --jq .login) --password-stdin
docker manifest inspect ghcr.io/monolithiclab/fngr:0.0.1-rc1
```
Expected: a manifest list with two entries (`linux/amd64`, `linux/arm64`).

Then check the `:latest` tag was NOT bumped:
Run: `docker manifest inspect ghcr.io/monolithiclab/fngr:latest 2>&1 || echo "no :latest yet (correct for pre-release)"`
Expected: error message (tag doesn't exist).

- [ ] **Step 8.6: Verify cosign signatures**

Run:
```bash
cosign verify ghcr.io/monolithiclab/fngr:0.0.1-rc1 \
  --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```
Expected: `Verification for ghcr.io/monolithiclab/fngr:0.0.1-rc1 -- The following checks were performed...` followed by JSON certificate details.

For the binary checksums:
```bash
gh release download v0.0.1-rc1 --repo monolithiclab/fngr --pattern 'SHA256SUMS*' --dir /tmp/fngr-rc1
cosign verify-blob \
  --signature /tmp/fngr-rc1/SHA256SUMS.sig \
  --certificate /tmp/fngr-rc1/SHA256SUMS.pem \
  --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  /tmp/fngr-rc1/SHA256SUMS
```
Expected: `Verified OK`.

- [ ] **Step 8.7: Verify Homebrew tap was NOT touched**

Run: `gh api /repos/monolithiclab/homebrew-tap/contents/Formula 2>&1 || echo "(no Formula directory yet — correct for pre-release)"`
Expected: 404 / no Formula directory (pre-release skips brew bump).

- [ ] **Step 8.8: If everything looks right, push the stable tag**

Run:
```bash
git tag v0.0.1
git push origin v0.0.1
```

- [ ] **Step 8.9: Watch the release workflow again**

Run: `gh run watch --repo monolithiclab/fngr`
Expected: passes in ~5–10 minutes.

- [ ] **Step 8.10: Verify the stable release adds `:latest` and the brew formula**

Run:
```bash
docker manifest inspect ghcr.io/monolithiclab/fngr:latest
gh api /repos/monolithiclab/homebrew-tap/contents/Formula/fngr.rb --jq .name
```
Expected: manifest list (multi-arch); `fngr.rb` exists in the tap.

Verify formula content:
Run: `gh api /repos/monolithiclab/homebrew-tap/contents/Formula/fngr.rb --jq .content | base64 -d | head -20`
Expected: a valid Homebrew formula referencing `https://github.com/monolithiclab/fngr/releases/download/v0.0.1/...` URLs.

- [ ] **Step 8.11: Flip ghcr.io package visibility to public**

The first ghcr.io push creates a private package. Flip it:

Run:
```bash
gh api -X PATCH /orgs/monolithiclab/packages/container/fngr --field visibility=public
```
(Requires a token with `read:packages, write:packages, admin:org` scopes; if `gh` complains about scopes, run `gh auth refresh -s read:packages,write:packages,admin:org` first.)

Verify from a logged-out machine (or `docker logout ghcr.io` first):
Run: `docker pull ghcr.io/monolithiclab/fngr:0.0.1`
Expected: pull succeeds without authentication.

- [ ] **Step 8.12: Sanity-test the published artifacts end-to-end**

```bash
# Homebrew (macOS only)
brew install monolithiclab/tap/fngr
fngr --version
brew uninstall fngr  # cleanup

# Docker
docker run --rm ghcr.io/monolithiclab/fngr:0.0.1 --version

# Tarball
gh release download v0.0.1 --repo monolithiclab/fngr --pattern 'fngr_*_linux_amd64.tar.gz' --dir /tmp/fngr-v0.0.1
tar xzf /tmp/fngr-v0.0.1/fngr_*_linux_amd64.tar.gz -C /tmp/fngr-v0.0.1
/tmp/fngr-v0.0.1/fngr --version
```
Expected: every command prints `v0.0.1`.

- [ ] **Step 8.13: Enable branch protection on `main`**

Via GitHub UI (Settings → Branches → Add rule for `main`):
- "Require status checks to pass before merging" ✓
- Required status checks: `test (ubuntu-latest)`, `test (macos-latest)`
- (Optional) "Require linear history", "Require pull request reviews"

Or via CLI:
```bash
gh api -X PUT /repos/monolithiclab/fngr/branches/main/protection \
  --field required_status_checks[strict]=true \
  --field 'required_status_checks[contexts][]=test (ubuntu-latest)' \
  --field 'required_status_checks[contexts][]=test (macos-latest)' \
  --field enforce_admins=false \
  --field required_pull_request_reviews= \
  --field restrictions=
```

Verify:
Run: `gh api /repos/monolithiclab/fngr/branches/main/protection --jq .required_status_checks.contexts`
Expected: `["test (ubuntu-latest)", "test (macos-latest)"]`

---

## Self-review checklist (after all tasks complete)

- [ ] `LICENSE` exists at repo root, MIT.
- [ ] `Dockerfile` is minimal distroless-based.
- [ ] `.goreleaser.yaml` validated by `goreleaser check` and snapshot build.
- [ ] CI workflow runs and passes on both matrix cells.
- [ ] Release workflow runs on `v0.0.1-rc1` (pre-release) and `v0.0.1` (stable) successfully.
- [ ] cosign signatures verify for both the SHA256SUMS file and the multi-arch container.
- [ ] Homebrew tap bumps only on stable tags; `:latest` Docker tag bumps only on stable tags.
- [ ] ghcr.io package is publicly pullable.
- [ ] Branch protection requires both CI cells green before merging to `main`.
- [ ] README documents `brew install` and `docker run` paths plus container caveats.
- [ ] Roadmap entries moved to Done; "Project infrastructure" section removed.

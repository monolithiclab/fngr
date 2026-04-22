# GitHub Actions: CI + Release pipeline — Design

**Status:** Draft
**Date:** 2026-04-22
**Roadmap items:** "Project infrastructure — GitHub Actions CI" + "GitHub Actions release"
(`docs/superpowers/roadmap.md`)

## Goal

Add two GitHub Actions workflows so every PR gets validated against
`make lint test` on a Linux + macOS matrix, and every `v*.*.*` tag
produces a signed, multi-channel release: GitHub Release with binaries
+ checksums + cosign signatures, a multi-arch container image on
`ghcr.io`, and a Homebrew formula update on `monolithiclab/homebrew-tap`.

## Non-goals

- Single-shot CI workflow with release jobs gated on tag presence
  (rejected: harder to read; gives nothing over two focused files).
- Hand-rolled cross-compile in YAML (rejected: GoReleaser is the
  established Go-CLI release pattern, and we want its built-in
  multi-channel support).
- Windows binaries (out of scope until a real user asks; no other
  platform-specific branches in `cmd/fngr`).
- GPG / PEM signing of artifacts (rejected: cosign keyless gives
  Sigstore-grade provenance with zero key management).
- A `latest-nonroot` Docker variant (out of scope for v1; default image
  runs as root so volume mounts inherit host permissions naturally).
- Coverage thresholds enforced as PR gate (project's standing rule is
  manual per-function check after every commit; CI just publishes the
  profile as an artifact).

## Architecture

Two workflow files plus one config file plus one Dockerfile, all at
canonical locations:

```
.github/workflows/ci.yml         # push-to-main + PR validation
.github/workflows/release.yml    # tag-triggered release pipeline
.goreleaser.yaml                 # single source of truth for the release
Dockerfile                       # consumed by GoReleaser's dockers: section
```

No production code changes. The Makefile already injects
`-X main.version` from `git describe`; GoReleaser uses the same
convention via its own `ldflags:` interpolation.

### Why GoReleaser

The release surface is non-trivial: 4 OS/arch binaries × archives ×
checksums × cosign signatures × multi-arch Docker manifest × Homebrew
formula × grouped changelog. Hand-rolled YAML for that is ~80+ lines
and re-implements every feature; GoReleaser's declarative config is
~80 lines that read top-to-bottom and grant first-class support for
all of the above. Adoption cost is one binary on the runner; nothing
in the production binary changes.

## Setup prerequisites (one-time, before merging)

0. **Add a `LICENSE` file at the repo root.** No LICENSE exists today;
   GoReleaser's `archives.files: [LICENSE, README.md]` would fail the
   build, and the brew formula's `license:` field needs a real SPDX
   identifier. Default assumption in this spec is **MIT**; the
   implementation plan must add `LICENSE` and update the brew config
   if a different license is chosen. (Open question: which license?
   See "Open questions" below.)

1. **Create the Homebrew tap repo:**
   ```bash
   gh repo create monolithiclab/homebrew-tap --public \
     --description "Homebrew tap for monolithiclab CLIs" \
     --add-readme
   ```

2. **Create a fine-grained PAT** scoped narrowly:
   - Repository access: only `monolithiclab/homebrew-tap`
   - Permissions: `Contents: Read and write`, `Metadata: Read-only`
   - Expiry: 1 year (calendar reminder for rotation)

3. **Store the PAT as an org-level secret** with selected-repo
   visibility:
   ```bash
   gh secret set HOMEBREW_TAP_TOKEN \
     --org monolithiclab \
     --visibility selected \
     --repos fngr \
     --body "<paste-from-step-2>"
   ```
   Future tap publishers (gomddoc, lightmeter…) get added to the
   `--repos` list rather than re-creating the secret.

4. **No other secrets needed.** `GITHUB_TOKEN` is automatic; cosign
   keyless reads its identity from the workflow's OIDC token; ghcr.io
   accepts `GITHUB_TOKEN` for `monolithiclab/*` packages.

## Component 1: `.github/workflows/ci.yml`

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

**Key choices:**

- **`concurrency`** kills in-flight CI when a PR is force-pushed; saves
  runner minutes.
- **`fail-fast: false`** lets us see both matrix cells' results; one
  failing OS shouldn't cancel the other.
- **`go-version-file: go.mod`** binds the toolchain to the project's
  declared version (currently 1.26.2); upgrading the project bumps CI
  in the same commit.
- **Built-in cache** (`cache: true`) keys on `go.sum` and covers
  `~/go/pkg/mod` + `~/.cache/go-build`.
- **`make lint test`** instead of `make ci`: the `codefix` and
  `format` targets mutate working-tree files (meant for local pre-
  commit), and the `lint` target's `gofmt -d .` step already enforces
  formatting in CI.
- **Coverage artifact** uploaded only from the ubuntu cell — duplicate
  uploads under the same name would overwrite. 14-day retention is
  enough to inspect a regression PR.

## Component 2: `.github/workflows/release.yml`

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
      - uses: docker/setup-qemu-action@v3   # multi-arch docker build
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

**Key choices:**

- **Tag pattern** `v*.*.*` and `v*.*.*-*` covers stable releases plus
  pre-releases (`-rc1`, `-beta.1`, etc.). GoReleaser auto-detects
  the suffix and marks the GitHub Release as pre-release.
- **Workflow-level `permissions:`** because there's only one job;
  per-job least-privilege is overkill here.
- **QEMU + Buildx** enable cross-arch Docker builds from a single
  ubuntu runner (no separate arm64 runner needed).
- **`docker/login-action`** against `ghcr.io` uses the workflow
  `GITHUB_TOKEN` — no credentials to manage.
- **`cosign-installer`** puts `cosign` on PATH so GoReleaser's
  `signs:` and `docker_signs:` blocks can call it.
- **`HOMEBREW_TAP_TOKEN`** is the only manually-configured secret.
- **`--clean`** wipes `dist/` before each run so failed prior runs
  don't pollute the workspace.

## Component 3: `.goreleaser.yaml`

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

**Key choices:**

- **`CGO_ENABLED=0`** is load-bearing — `modernc.org/sqlite` is pure-Go
  but we want explicit static builds; this also keeps the Docker
  scratch/distroless image working.
- **`-s -w`** strip symbol table and DWARF for ~30% binary-size
  reduction on release builds. Local `make build` keeps debug info.
- **License declared MIT** — assumed; LICENSE file must be added per
  setup step 0 before the workflow can build. Update this field if a
  different SPDX identifier is chosen.
- **Two `dockers:` blocks** (one per arch) feed two
  `docker_manifests:` (one versioned, one `:latest` with
  `skip_push: auto`) so multi-arch resolves to a single tag.
- **`skip_upload: auto`** on the brew formula and `skip_push: auto`
  on `:latest` Docker tag both honor pre-release semantics: rc/beta/
  alpha tags don't bump stable channels.
- **Brew formula `test:` block** is mandatory in Homebrew; we just
  assert `fngr --version` runs and contains the project name.
- **Changelog excludes `chore:`** (build/CI noise) and merge commits;
  remaining commits group under Features / Bug fixes / Documentation /
  Other based on Conventional Commit prefix (the project's existing
  commit style already matches).

## Component 4: `Dockerfile`

```dockerfile
# Image is built by GoReleaser. The binary is cross-compiled outside this
# Dockerfile and made available in the build context as `fngr`.
FROM gcr.io/distroless/static-debian13

COPY fngr /fngr

ENTRYPOINT ["/fngr"]
```

**Key choices and container-usage caveats** (will be added to README
as a "Container usage" subsection):

- **Image is ~6 MB total** (~2 MB distroless base + ~4 MB stripped
  fngr binary).
- **`distroless/static-debian13`** vs `scratch`: distroless includes
  CA certs, `/usr/share/zoneinfo`, and `/tmp`, so timezone names work
  natively (`-e TZ=America/New_York`) and any future TLS-touching
  feature works without rebuild. Trade-off is ~2 MB additional base
  size, accepted.
- **DB path requires a volume mount.** Typical invocation:
  `docker run --rm -v "$HOME/.fngr.db:/data/fngr.db" -e FNGR_DB=/data/fngr.db ghcr.io/monolithiclab/fngr:latest -S '#ops'`
- **Default user is root** so volume permissions match the host's
  default ownership. Non-root variant deferred to a future
  `latest-nonroot` tag if a user asks.
- **Interactive features disabled.** `fngr add -e` (editor) needs
  `$EDITOR` and a binary inside the image — not provided. The pager
  in `fngr list` needs `less` + a TTY — also absent. Container is for
  scripted use only (`add "note"`, `--format=json | jq`, etc.).

## Post-first-release setup (one-time)

After the first `v0.1.0` tag pushes successfully:

5. **Set the ghcr.io package visibility to public:**
   - Browse to `https://github.com/orgs/monolithiclab/packages/container/fngr/settings`
   - Under "Danger Zone" → "Change visibility" → set to Public.
   - Equivalent CLI:
     `gh api -X PATCH /orgs/monolithiclab/packages/container/fngr --field visibility=public`
     (requires `read:packages, write:packages, admin:org` scopes on
     the calling token).

6. **Enable branch protection on `main`:**
   - Settings → Branches → Add rule for `main` → "Require status
     checks to pass" → select `CI / test (ubuntu-latest)` and
     `CI / test (macos-latest)`.
   - Optionally add "Require linear history" + "Require pull request
     reviews" if you want to formalize the workflow.

## Testing

Workflow YAML doesn't have unit tests; validation runs in three layers:

### Local rehearsal (before the workflow PR even lands)

- `goreleaser check` validates `.goreleaser.yaml` parses + every block
  is internally consistent. Fast and free.
- `goreleaser release --snapshot --skip=publish,sign --clean` — full
  local rehearsal: cross-compiles the four binaries, builds both
  Docker arches, generates the changelog. Confirms config produces
  what we expect without pushing or signing.
- Optional: `gh act -n -j test` to dry-run the CI workflow with `act`.

### CI workflow self-test (immediate)

Once `ci.yml` lands on `main`, the very next push runs it.
Acceptance: both matrix cells pass and the coverage artifact uploads.

### Release workflow validation via pre-release tag

Push `v0.0.1-rc1` once setup steps 1–4 are complete. Expected:

- GitHub Release created, marked **pre-release**, with binaries +
  `SHA256SUMS` + `SHA256SUMS.sig` + `SHA256SUMS.pem` attached.
- `ghcr.io/monolithiclab/fngr:0.0.1-rc1` exists (multi-arch) and is
  cosign-signed:
  ```bash
  cosign verify ghcr.io/monolithiclab/fngr:0.0.1-rc1 \
    --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
  ```
- **No** `:latest` Docker tag bump (skip_push: auto fired).
- **No** Homebrew formula update (skip_upload: auto fired).
- Changelog grouped under Features / Bug fixes / Documentation /
  Other; `chore:` and merge commits excluded.

If rc1 looks good, push `v0.0.1` (no suffix). Expected: same
artifacts, plus `:latest` tag bump, plus a `fngr.rb` formula commit
on `monolithiclab/homebrew-tap`.

### Sanity-test the published artifacts (post-release)

- `brew install monolithiclab/tap/fngr && fngr --version` → matches the tag.
- `docker run --rm ghcr.io/monolithiclab/fngr:0.0.1 --version` → matches the tag.
- Binary verification:
  ```bash
  curl -L https://github.com/monolithiclab/fngr/releases/download/v0.0.1/SHA256SUMS \
    | sha256sum -c -
  cosign verify-blob \
    --signature SHA256SUMS.sig --certificate SHA256SUMS.pem \
    --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
    SHA256SUMS
  ```
- After flipping ghcr.io visibility to public: `docker pull
  ghcr.io/monolithiclab/fngr:0.0.1` from a logged-out machine.

## Edge cases

| Case                                     | Behavior                                                                  |
| ---------------------------------------- | ------------------------------------------------------------------------- |
| PR from a fork                           | CI runs with limited token; release secrets unavailable (correct & safe). |
| Tag pushed without a release commit      | Workflow runs anyway; GoReleaser builds from the tagged SHA.              |
| Tag re-push (force-push to overwrite)    | Workflow runs again; GoReleaser will refuse to overwrite an existing release without `--force`. We don't pass `--force`, so re-pushes fail loudly — by design. |
| Pre-release tag (`v0.1.0-rc1`)           | GitHub Release marked pre-release; no `:latest` Docker tag; no brew bump. |
| Local `make build` after this lands       | Unchanged. Makefile still injects `git describe`-derived version.         |
| Linter version drift in CI                | `make lint` auto-installs latest linters via `go install`. CI inherits the same behavior; flaky linter releases would surface as CI failures. Acceptable trade-off vs. pinning every linter.  |
| ghcr.io package starts private            | First-release step 5 flips it public. Until then, `docker pull` requires a `gh auth login` + `docker login ghcr.io`. |

## Open questions

1. **Which license?** No `LICENSE` file exists today. The spec
   assumes MIT (most common for personal Go CLIs and compatible with
   GoReleaser's brew config). If a different SPDX (Apache-2.0,
   BSD-3-Clause, …) is preferred, update both the LICENSE file added
   in setup step 0 and the brew `license:` field. **Decision needed
   before the implementation plan starts.**

Decisions resolved during brainstorming:

- GoReleaser as the release engine.
- Three distribution channels (GitHub Releases + ghcr.io + Homebrew tap).
- Cosign keyless signing.
- Pre-release tag support (`v*.*.*-*` triggers, `skip_*: auto` honors).
- Conventional-Commits-grouped changelog.
- Org-level secret with selected-repo visibility for `HOMEBREW_TAP_TOKEN`.
- distroless static-debian13 as Docker base (CA certs + tzdata).

## Out of scope (deliberate)

- **Hand-rolled YAML cross-compile** — see Non-goals.
- **GPG signing** — see Non-goals.
- **Windows binaries** — see Non-goals.
- **Coverage gating** — see Non-goals.
- **Per-job least-privilege** — overkill for a single-job release
  workflow; revisit if a second job appears.
- **`latest-nonroot` Docker variant** — wait for a real user request.
- **`_ "time/tzdata"` import** — distroless static-debian13 already
  ships zoneinfo; binary-embedded tzdata is unnecessary right now.
  Revisit if the Docker base ever shrinks to scratch.
- **Codeowners / CODEOWNERS file** — single-author project; revisit
  if contributor count grows.
- **Dependabot** — separate decision; not load-bearing for the CI/
  release pipeline. Track as a follow-up roadmap item.
- **govulncheck step in CI** — same. Adds value but trades against
  CI flakiness when CVEs publish for transitive deps.
- **Release-please / semantic-release** — auto-bumping the version
  from commit messages. Manual `git tag` works fine at this scale.

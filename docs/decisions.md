# Decisions

Append-only and D-numbered. Never edit an entry: supersede it with a new one that names the old number. Each entry
records what changed, the evidence for it, and what it costs if it proves wrong.

## D1 — 2026-10-07: where the earlier decisions live

Before this log existed, fngr recorded its decisions beside the work they shaped. They stay there, and this entry
indexes them rather than copying them:

- **Each design spec** under `docs/superpowers/specs/`, in its "Goals", "Non-goals" / "Out of scope" and
  "Architecture" sections. The most load-bearing are in `2026-04-16-cli-design.md` (SQLite via modernc, Kong, the
  data model), `2026-04-18-list-ux-overhaul-design.md` (`fngr` ≡ `fngr list`, streaming renderers, the pager),
  `2026-04-20-add-json-import-and-meta-shape-design.md` (the `[[key, value], ...]` meta wire shape),
  `2026-04-22-github-actions-design.md` "Why GoReleaser", and `2026-04-23-title-body-split-design.md` "Split rule".
- **`docs/architecture.md`**: every file's entry records why its code has the shape it has, usually naming the bug or
  review finding that forced it. Read the entry before changing the code it describes.
- **`docs/publishing.md`** "Gotchas": the release-pipeline decisions (repo-level tap secret, the `nonroot` image and
  directory mounts, keeping GoReleaser's deprecated `dockers:` and `brews:` keys).
- **`docs/reviews/2026-07-27-deep-audit.md`**: the won't-fix and documented-not-fixed rulings on each finding.

## D2 — 2026-10-07: golangci-lint is the only Go linter

**Changed.** `make lint` ran gofmt, vet, staticcheck, golangci-lint, gosec and gocritic as six steps with six pinned
tools and a `staticcheck.conf`. It now runs golangci-lint v2 once with the lab's canonical `.golangci.yml` (gofmt -s,
govet, staticcheck with ST1000, gosec outside `_test.go`, gocritic's `builtinShadow` / `importShadow`), plus
govulncheck, `go mod tidy -diff` and the pin check.

**Evidence.** On gomddoc the single config found exactly what the six found, on clean code and on nine planted
violations; on fngr it reported nothing new on code the six already passed (the lab's go-cli-development skill
records the comparison).

**If wrong.** A class of finding one standalone tool caught and the config misses goes unreported. The fix is in the
canonical `golangci.yml`, re-synced into every Go repo, not a local override.

## D3 — 2026-10-07: releases are signed with a cosign v3 Sigstore bundle

**Changed.** The release job installs cosign v3 (`sigstore/cosign-installer` v4.1.2) and signs `SHA256SUMS` into one
`SHA256SUMS.sigstore.json` bundle with `cosign sign-blob --bundle`, replacing the detached `SHA256SUMS.sig` and
`SHA256SUMS.pem` that cosign v2 wrote. Every archive and SBOM is listed in `SHA256SUMS`, so the one bundle covers them.

**Evidence.** cosign v3 deprecates `--output-signature` / `--output-certificate`; holding the installer on v3 to keep
them meant staying on an unmaintained cosign line. gomddoc made the same move on 2026-09-25 and has released with it.

**If wrong.** A user on cosign older than v2.4 cannot read `--bundle`; they upgrade cosign. Releases up to v0.0.4 keep
their `.sig`/`.pem` pair, and `docs/publishing.md` keeps the command that verifies them.

# fngr

A command-line journal for logging and tracking events. Events support
parent-child trees, key-value metadata with `@person` and `#tag` shorthands,
and full-text search.

Data is stored in a single SQLite file (pure-Go, no CGo).

```
fngr add "Deployed v2 to staging #ops @alice"
fngr -S '#ops' --from yesterday
fngr event 12 -t
```

## Status

Released and in daily use: v0.0.4 (2026-08-31) ships through Homebrew, a
signed multi-arch image on ghcr.io and signed tarballs. The 2026-07-27 deep
audit is closed; what it left open is parked in [ROADMAP.md](ROADMAP.md).
The next release is the first signed with a cosign v3 Sigstore bundle
(`SHA256SUMS.sigstore.json`) instead of a detached `.sig`/`.pem` pair.

## Install

### Homebrew (macOS / Linux)

```
brew install monolithiclab/tap/fngr
```

### Go install

```
go install github.com/monolithiclab/fngr/cmd/fngr@latest
```

### Pre-built binaries

Download the right tarball for your OS/arch from the
[releases page](https://github.com/monolithiclab/fngr/releases).
Every release attaches `SHA256SUMS` and its cosign Sigstore bundle,
`SHA256SUMS.sigstore.json` (signature, certificate and transparency-log
proof in one file; needs cosign v2.4 or later). Verify with:

```
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  SHA256SUMS
sha256sum -c SHA256SUMS
```

Each tarball also ships an SPDX SBOM as
`<tarball>.sbom.json`. The SBOMs are listed in `SHA256SUMS`, so the
signature above covers them too. Releases up to v0.0.4 predate the bundle
and carry `SHA256SUMS.sig` and `SHA256SUMS.pem` instead
([docs/publishing.md](docs/publishing.md) has the older command).

### Build from source

Needs Go 1.26 (go.mod's `toolchain` line pins the exact patch; `make`
fetches it if yours differs).

```
make build        # binary at build/fngr
make install      # installs to $GOBIN
```

## Usage

`fngr help` (or `fngr <command> --help`) lists every flag. The manual, with
examples for every command, the `-S` filter grammar, the JSON import format,
exit codes, the database location, running in a container and
troubleshooting, is [docs/usage.md](docs/usage.md).

## Development

```
make help         # every target
make ci           # the gate CI runs: golangci-lint, govulncheck, go mod tidy -diff, pin check, tests with -race
```

## Documentation

| Document | What it covers |
| --- | --- |
| [docs/usage.md](docs/usage.md) | The user manual |
| [docs/architecture.md](docs/architecture.md) | File-by-file design and the reasons behind it |
| [docs/publishing.md](docs/publishing.md) | The release pipeline, pin refreshes, and its gotchas |
| [docs/decisions.md](docs/decisions.md) | Decision log |
| [docs/superpowers/](docs/superpowers/) | Dated design specs and implementation plans |
| [docs/reviews/](docs/reviews/) | Review rounds |
| [ROADMAP.md](ROADMAP.md) | Ideas, parked follow-ups and open review findings |

## License

MIT © Monolithic Lab. See [LICENSE](LICENSE).

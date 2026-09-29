# Development

Requires Go 1.26+.

```sh
make build     # build ./quet with the version stamped in
make install   # install quet into $GOPATH/bin
make check     # gofmt + go vet + go test
make race      # the test suite under the race detector
make release   # cross-compile dist/ artifacts with SHA256SUMS
make help      # list every target
```

The longhand equivalents are `go build -o quet ./cmd/quet`, `go test ./...`, `go vet ./...` and
`gofmt -l .`.

## Layout

```
cmd/quet/                command line entry point and argument parsing
internal/corpus/         .jsonl/.json/.txt loading, stable ids, JSON re-encoding
internal/config/         quet.yaml and flags.yaml
internal/checks/         the six deterministic diagnostics
internal/review/         review session: states, filters, undo, counts, search
internal/storage/        SQLite sidecar (WAL, immediate writes)
internal/export/         the five export presets, atomic file writing
internal/tui/            Bubble Tea interface: file browser and review screen
internal/version/        build identity stamped in at link time
flags.yaml               manual flag taxonomy (the default search location)
configs/                 copyable starting-point quet.yaml
examples/                small example corpora in all three formats
docs/                    user documentation
install.sh               curl | sh installer, attached to every release
.github/workflows/       CI, release-please, and the publish workflow
release-please-config.json, .release-please-manifest.json   release-please settings and current version
```

Tests live next to the code they cover (`internal/<package>/*_test.go`, `cmd/quet/*_test.go`); they
are deterministic and never touch the network. `internal/export/export_test.go` covers preset shapes,
default output paths, and a full review → export round trip.

`examples/corpus.jsonl` is a test fixture: `internal/corpus/corpus_test.go` asserts its record count
and that loading leaves it byte-identical, and the docs quote its `stats` and export output. Update
all three when changing it.

## CI

Every push runs `.github/workflows/ci.yml`: `gofmt`, `go vet`, shellcheck on the shell scripts, the
tests under the race detector, and `make release`. The four binaries are kept for a week as a
workflow artifact, so any commit can be tried without cutting a release.

## Releasing

Releases are automated with [release-please](https://github.com/googleapis/release-please); nobody
tags by hand.

1. Commit to `main` with [conventional commits](https://www.conventionalcommits.org/). Below 1.0,
   `feat:`, `fix:` and `perf:` bump the patch version and a breaking change (`feat!:`) bumps the
   minor. `chore:`, `docs:`, `test:`, `refactor:`, `ci:` and `build:` release nothing.
2. release-please keeps one Release PR open (`chore: release x.y.z`). It updates `CHANGELOG.md` from
   the commit subjects and bumps the version in `internal/version/version.go` and
   `.release-please-manifest.json`.
3. Merging that PR tags `vx.y.z` and creates the GitHub Release. The same run then calls
   `.github/workflows/publish.yml`, which checks out the tag, runs `make check` and `make release`,
   confirms the binary reports the right version, and attaches `quet_<os>_<arch>`, `SHA256SUMS`
   and `install.sh` to the release.

To re-publish a tag, run the Publish workflow by hand from the Actions tab with that tag. Uploads use
`--clobber`, so re-running replaces the assets. It builds from the tagged tree, so it only works for
tags that contain `install.sh`: 0.1.1 onwards, not `v0.1.0`.

One-time repository setup:

- Settings → Actions → General → enable "Allow GitHub Actions to create and approve pull requests",
  so release-please can open the Release PR.

Version metadata is injected at link time from `internal/version`, so a build made inside a checkout
reports the tag or revision it came from:

```sh
$ make build && ./quet --version
quet 0.1.0 (e2b893f 2026-09-29)
```

A plain `go build ./cmd/quet` reports the bare version (`quet 0.1.0`). Release builds set
`CGO_ENABLED=0`; the SQLite driver is pure Go, so the binaries stay static and cross-compile
without a toolchain.

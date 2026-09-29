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
internal/checks/         the seven deterministic auto checks
internal/review/         review session: states, filters, undo, counts, search
internal/storage/        SQLite sidecar (WAL, immediate writes)
internal/export/         the five export presets, atomic file writing
internal/tui/            Bubble Tea interface: file browser and review screen
internal/version/        build identity stamped in at link time
flags.yaml               manual flag taxonomy (the default search location)
configs/                 copyable starting-point quet.yaml
examples/                small example corpora in all three formats
docs/                    user documentation
```

Tests live next to the code they cover (`internal/<package>/*_test.go`, `cmd/quet/*_test.go`); they
are deterministic and never touch the network. `internal/export/export_test.go` covers preset shapes,
default output paths, and a full review → export round trip.

`examples/corpus.jsonl` is a test fixture: `internal/corpus/corpus_test.go` asserts its record count
and that loading leaves it byte-identical, and the docs quote its `stats` and export output. Update
all three when changing it.

## Releasing

Quet is a single static binary with no runtime dependencies, so a release is a tag plus a set of
cross-compiled artifacts.

1. Move the `## [Unreleased]` entries in `CHANGELOG.md` into a new version section and commit that.
2. Tag the release: `git tag -a v0.1.0 -m "Quet 0.1.0"`.
3. Build the artifacts: `make release` writes `dist/quet_<os>_<arch>` for darwin and linux on arm64
   and amd64, plus `dist/SHA256SUMS`.
4. Push the tag and attach everything in `dist/` to the release.

Version metadata is injected at link time from `internal/version`, so a build made inside a checkout
reports the tag or revision it came from:

```sh
$ make build && ./quet --version
quet 0.1.0 (e2b893f 2026-09-29)
```

A plain `go build ./cmd/quet` reports the bare version (`quet 0.1.0`). Release builds set
`CGO_ENABLED=0`; the SQLite driver is pure Go, so the binaries stay static and cross-compile
without a toolchain.

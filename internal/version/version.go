// Package version carries the build identity of the quet binary.
//
// The defaults describe an unreleased developer build. Release builds override
// them at link time, which is what `make build` and `make release` do:
//
//	go build -ldflags "-X github.com/8bu/quet/internal/version.Version=0.2.0" ./cmd/quet
package version

import "strings"

// Version is the semantic version, without a leading "v". release-please bumps
// it in the Release PR, so a plain `go build` of a tag reports that tag.
var Version = "0.1.0" // x-release-please-version

// Commit is the short git revision the binary was built from, when known.
var Commit = ""

// Date is the build date in YYYY-MM-DD form, when known.
var Date = ""

// String renders the build identity for `quet --version` (and for the TUI
// header). A plain `go build` yields "0.1.0"; a release build embeds the
// revision and date, yielding "0.1.0 (e2b893f 2026-09-29)".
func String() string {
	var extra []string
	if Commit != "" {
		extra = append(extra, Commit)
	}
	if Date != "" {
		extra = append(extra, Date)
	}
	if len(extra) == 0 {
		return Version
	}
	return Version + " (" + strings.Join(extra, " ") + ")"
}

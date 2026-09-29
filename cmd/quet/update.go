package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/update"
	"github.com/8bu/quet/internal/version"
)

const (
	// updateTimeout bounds `quet update`: the version lookup plus the download.
	updateTimeout = 30 * time.Second
	// checkTimeout bounds the background update check of a normal run.
	checkTimeout = 5 * time.Second
	// noticeWait caps how long a one-shot command waits for the update check,
	// counted from when the check started.
	noticeWait = 1500 * time.Millisecond
)

// runUpdate replaces the running binary with the latest release, or with
// --check only reports whether one is available.
func runUpdate(cmd command, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	c := update.New()
	latest, err := c.Latest(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	if !update.Newer(version.Version, latest) {
		fmt.Fprintf(stdout, "quet %s is up to date\n", version.Version)
		return 0
	}
	if cmd.check {
		fmt.Fprintf(stdout, "quet %s is available (you have %s); run: quet update\n", latest, version.Version)
		return 0
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "quet: update: locating the running binary: %v\n", err)
		return 1
	}
	if err := c.Install(ctx, latest, exe); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "updated quet %s → %s (%s)\n", version.Version, latest, exe)
	return 0
}

// pendingCheck is a background lookup of the latest release. A nil
// *pendingCheck means the check is off; its methods then do nothing.
type pendingCheck struct {
	start  time.Time
	done   chan struct{} // closed once latest and err are set
	latest string
	err    error
}

// startCommandCheck starts the update check for cmd when its configuration
// turns update.check on. A configuration that fails to load skips the check.
func startCommandCheck(cmd command) *pendingCheck {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil
	}
	return startUpdateCheck(cfg)
}

// startUpdateCheck looks up the latest release in the background when
// cfg.Update.Check is set, and returns nil (no network) otherwise.
func startUpdateCheck(cfg config.Config) *pendingCheck {
	if !cfg.Update.Check {
		return nil
	}
	p := &pendingCheck{start: time.Now(), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		p.latest, p.err = update.New().Latest(ctx)
	}()
	return p
}

// result reports the newer release once the lookup has finished; ok is false
// when the lookup failed or the running version is current.
func (p *pendingCheck) result() (latest string, ok bool) {
	if p.err != nil || !update.Newer(version.Version, p.latest) {
		return "", false
	}
	return p.latest, true
}

// tuiCheck is the TUI's update check: it blocks until the lookup finishes.
// It is nil when the check is off.
func (p *pendingCheck) tuiCheck() func() (string, bool) {
	if p == nil {
		return nil
	}
	return func() (string, bool) {
		<-p.done
		return p.result()
	}
}

// notify prints the update notice on stderr when a newer release exists,
// waiting for the lookup at most noticeWait since it started. Failures and
// slow lookups are silent.
func (p *pendingCheck) notify(stderr io.Writer) {
	if p == nil {
		return
	}
	if !p.wait(noticeWait - time.Since(p.start)) {
		return
	}
	if latest, ok := p.result(); ok {
		fmt.Fprintf(stderr, "quet: %s is available (you have %s); run: quet update\n", latest, version.Version)
	}
}

// wait reports whether the lookup finishes within d. A lookup that has
// already finished counts even when d has run out.
func (p *pendingCheck) wait(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	default:
	}
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.done:
		return true
	case <-timer.C:
		return false
	}
}

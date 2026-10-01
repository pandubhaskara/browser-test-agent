// Package preview renders one page for the editor's step authoring view: the
// start URL opened in a real browser with the test's options, and the
// resulting DOM returned as HTML. No steps run; nothing is kept on disk.
package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/browser"
	"browser-test-agent/internal/home"
	"browser-test-agent/internal/runner"
	"browser-test-agent/pkg/spec"
)

// Config is what the command line supplies.
type Config struct {
	Stdout    io.Writer
	DriverDir string
}

// settleTimeout is how long, after `load`, the page gets to go network-idle
// so a single-page app has painted. Best effort: a page that keeps polling
// never goes idle, and is snapshotted as is.
const settleTimeout = 3 * time.Second

// Run renders the spec's start URL and writes the preview events. The exit
// code follows `run`: 0 whether or not the page could be rendered (the
// verdict is in the events), non-zero only when the runner itself failed.
func Run(ctx context.Context, cfg Config, s *spec.RunSpec) int {
	// A preview is a run with no steps and no artifacts.
	s.Steps = nil
	s.Retries = 0
	s.Options.CaptureHTTPPayloads = false
	s.Options.NoScreenshots = true
	if err := s.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid RunSpec: %v\n", err)
		return runner.ExitInfra
	}

	scope := runner.NewScope(s)
	mask := scope.Masker().Mask
	fail := func(msg string) int {
		_ = spec.EncodePreviewError(cfg.Stdout, mask(msg))
		return runner.ExitOK
	}

	if err := runner.ResolveLocals(scope, s); err != nil {
		return fail(err.Error())
	}

	runDir, err := home.RunDir("preview-" + s.RunID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runner.ExitInfra
	}
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "create preview folder %s: %v\n", runDir, err)
		return runner.ExitInfra
	}
	defer os.RemoveAll(runDir)

	launched, err := browser.Launch(cfg.DriverDir, s)
	if err != nil {
		var infra *browser.InfraError
		if errors.As(err, &infra) {
			fmt.Fprintln(os.Stderr, mask(err.Error()))
			return runner.ExitInfra
		}
		return fail(err.Error())
	}
	defer launched.Close()

	session, err := browser.NewContext(launched, s, runDir)
	if err != nil {
		var infra *browser.InfraError
		if errors.As(err, &infra) {
			fmt.Fprintln(os.Stderr, mask(err.Error()))
			return runner.ExitInfra
		}
		return fail(err.Error())
	}
	defer session.Close()
	session.Net.Scrub = mask

	startURL, _, err := scope.Resolve(s.StartURL)
	if err != nil {
		return fail(err.Error())
	}
	timeout := runner.DefaultStartURLTimeout
	if s.Options.StartURLTimeoutSec > 0 {
		timeout = time.Duration(s.Options.StartURLTimeoutSec) * time.Second
	}

	page := session.Page
	resp, err := page.Goto(startURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(float64(timeout / time.Millisecond)),
		WaitUntil: playwright.WaitUntilStateLoad,
	})
	if err != nil {
		return fail(fmt.Sprintf("could not open %s: %v", startURL, err))
	}
	// Let a client-rendered page paint. Failure here is not a failure.
	_ = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(float64(settleTimeout / time.Millisecond)),
	})

	html, err := content(page)
	if err != nil {
		return fail(fmt.Sprintf("could not read the page: %v", err))
	}
	if len(html) > spec.PreviewMaxHTMLBytes {
		return fail(fmt.Sprintf("the page is larger than %d MB", spec.PreviewMaxHTMLBytes>>20))
	}
	title, _ := page.Title()
	meta := spec.PreviewEvent{URL: mask(page.URL()), Title: mask(title)}
	if resp != nil {
		meta.Status = resp.Status()
	}
	if err := spec.EncodePreview(cfg.Stdout, meta, mask(html)); err != nil {
		fmt.Fprintf(os.Stderr, "write preview: %v\n", err)
		return runner.ExitInfra
	}
	return runner.ExitOK
}

// content reads the DOM, retrying once: a script that navigates right after
// load destroys the execution context mid-read.
func content(page playwright.Page) (string, error) {
	html, err := page.Content()
	if err == nil {
		return html, nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "context") {
		return "", err
	}
	time.Sleep(250 * time.Millisecond)
	return page.Content()
}

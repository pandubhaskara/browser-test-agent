// Package runner executes one RunSpec end to end: launch, attempts with
// retries, steps with screenshots, events, artifacts.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/browser"
	"browser-test-agent/internal/home"
	"browser-test-agent/internal/output"
	"browser-test-agent/internal/steps"
	"browser-test-agent/pkg/spec"
	"browser-test-agent/pkg/vars"
)

// Defaults the RunSpec may override per step or per test.
const (
	DefaultStepTimeout     = 30 * time.Second
	DefaultStartURLTimeout = 60 * time.Second
	DefaultRetryWait       = 300 * time.Millisecond
	// startStepID is the pseudo-step for the initial navigation: the plan has
	// a failed start URL "fail step 0", and giving it an ID lets the UI show
	// it like any other step.
	startStepID = "start_url"
)

// Config is what the command line supplies.
type Config struct {
	Stdout        io.Writer
	RunnerVersion string
	DriverDir     string
}

// Exit codes. Test pass and test fail are both 0: the verdict is in the
// events. Non-zero means the runner itself could not do its job, which the
// worker files as `error` — never counted toward alerts.
const (
	ExitOK    = 0
	ExitInfra = 1
)

// Run executes the spec and returns the process exit code.
func Run(ctx context.Context, cfg Config, s *spec.RunSpec) int {
	if err := s.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid RunSpec: %v\n", err)
		return ExitInfra
	}

	runDir, err := home.RunDir(s.RunID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitInfra
	}
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "create run folder %s: %v\n", runDir, err)
		return ExitInfra
	}

	// The masker is created before anything is emitted and seeded with every
	// secure value the spec carries, so the very first line is already safe.
	scope := NewScope(s)
	emit := output.New(cfg.Stdout, filepath.Join(runDir, spec.ResultFile), s.RunID, scope.Masker())
	defer emit.Close()

	if err := ResolveLocals(scope, s); err != nil {
		emit.Emit(spec.Event{Ev: spec.EvRunFinished, Status: spec.StatusError, Error: err.Error()})
		return ExitInfra
	}

	launched, err := browser.Launch(cfg.DriverDir, s)
	if err != nil {
		emit.Emit(spec.Event{Ev: spec.EvRunFinished, Status: spec.StatusError, Error: err.Error()})
		return ExitInfra
	}
	defer launched.Close()

	emit.Emit(spec.Event{
		Ev: spec.EvRunStarted, Attempt: 1,
		Browser: launched.Name, BrowserVersion: launched.Version, Device: s.Device, RunnerVersion: cfg.RunnerVersion,
	})

	runStart := time.Now()
	attempts := 1 + s.Retries
	retryWait := DefaultRetryWait
	if s.RetryWaitMs > 0 {
		retryWait = time.Duration(s.RetryWaitMs) * time.Millisecond
	}

	var final attemptResult
	for attempt := 1; attempt <= attempts; attempt++ {
		emit.Emit(spec.Event{Ev: spec.EvAttemptStarted, Attempt: attempt})
		final = runAttempt(ctx, emit, launched, s, scope, runDir, attempt)
		emit.Emit(spec.Event{Ev: spec.EvAttemptFinished, Attempt: attempt, Status: final.status, FailedStepID: final.failedStepID, Error: final.err})

		if final.status != spec.StatusFailed || attempt == attempts {
			break
		}
		// A retry gets a fresh browser context (runAttempt opens one) so the
		// failed attempt's cookies and storage cannot carry into it.
		emit.Log("attempt %d failed at %s; retrying in %s", attempt, final.failedStepID, retryWait)
		select {
		case <-time.After(retryWait):
		case <-ctx.Done():
			final = attemptResult{status: spec.StatusError, err: ctx.Err().Error()}
			attempt = attempts
		}
	}

	finished := spec.Event{
		Ev:           spec.EvRunFinished,
		Status:       final.status,
		FailedStepID: final.failedStepID,
		Error:        final.err,
		DurationMs:   time.Since(runStart).Milliseconds(),
		Result:       spec.ResultFile,
	}
	if final.resources != "" {
		finished.Resources = final.resources
	}
	emit.Emit(finished)

	if final.status == spec.StatusError {
		return ExitInfra
	}
	return ExitOK
}

// NewScope builds the variable scope for a spec: globals set, and the masker
// seeded with every secure value the spec carries (globals, client key,
// HTTP password), so output is safe before anything runs. Shared with the
// preview command.
func NewScope(s *spec.RunSpec) *vars.Scope {
	mask := vars.NewMasker()
	scope := vars.NewScope(s.TestID, s.RunID, time.Now(), mask)
	for name, v := range s.GlobalVars {
		scope.Set(name, v.V, v.Secure)
	}
	if s.ClientCert != nil {
		mask.Add(s.ClientCert.KeyPEM)
		mask.Add(s.ClientCert.Passphrase)
	}
	if s.Options.HTTPAuth != nil {
		mask.Add(s.Options.HTTPAuth.Password)
	}
	return scope
}

// ResolveLocals resolves the spec's local variables into scope now, so
// date/timestamp builtins reflect the start and every attempt sees the same
// values.
func ResolveLocals(scope *vars.Scope, s *spec.RunSpec) error {
	for _, lv := range s.LocalVars {
		val, _, err := scope.Resolve(lv.Value)
		if err != nil {
			return fmt.Errorf("local variable %s: %v", lv.Name, err)
		}
		scope.Set(lv.Name, val, lv.Secure)
	}
	return nil
}

// attemptResult is one attempt's verdict.
type attemptResult struct {
	status       string
	failedStepID string
	err          string
	resources    string
}

// runAttempt opens a fresh context, navigates to the start URL, and runs the
// steps in order. It always closes the context, and always tries to write
// resources.json, so a failed attempt leaves the same artifacts as a passed one.
func runAttempt(ctx context.Context, emit *output.Emitter, l *browser.Launched, s *spec.RunSpec, scope *vars.Scope, runDir string, attempt int) attemptResult {
	session, err := browser.NewContext(l, s, runDir)
	if err != nil {
		var infra *browser.InfraError
		if errors.As(err, &infra) {
			return attemptResult{status: spec.StatusError, err: err.Error()}
		}
		// A bad option value (an unparsable cookie, say) is the test's fault,
		// and reads as a failure of step 0.
		emit.Emit(spec.Event{Ev: spec.EvStepFinished, Attempt: attempt, StepID: startStepID, StepType: spec.StepGoToURL, StepName: "Open start URL", Status: spec.StatusFailed, Error: err.Error()})
		return attemptResult{status: spec.StatusFailed, failedStepID: startStepID, err: err.Error()}
	}
	defer session.Close()

	session.Net.Scrub = scope.Masker().Mask
	session.Net.WatchDownloads(session.Page)

	// A click that opens a new tab makes that tab the active page, as a user
	// would experience it. The popup is watched for downloads too.
	current := session.Page
	session.Context.OnPage(func(p playwright.Page) {
		_ = p.WaitForLoadState()
		session.Net.WatchDownloads(p)
		current = p
	})

	stepCtx := &steps.Context{
		Ctx:     ctx,
		Browser: session.Context,
		Scope:   scope,
		Net:     session.Net,
		RunDir:  runDir,
		Log:     emit.Log,
	}

	result := attemptResult{status: spec.StatusPassed}
	screenshotIndex := 0

	// Step 0: the start URL.
	startTimeout := DefaultStartURLTimeout
	if s.Options.StartURLTimeoutSec > 0 {
		startTimeout = time.Duration(s.Options.StartURLTimeoutSec) * time.Second
	}
	session.Net.SetStep(startStepID)
	startAt := time.Now()
	startURL, _, resolveErr := scope.Resolve(s.StartURL)
	var navErr error
	if resolveErr != nil {
		navErr = resolveErr
	} else {
		_, navErr = current.Goto(startURL, playwright.PageGotoOptions{
			Timeout:   playwright.Float(float64(startTimeout / time.Millisecond)),
			WaitUntil: playwright.WaitUntilStateLoad,
		})
	}
	startEv := spec.Event{Ev: spec.EvStepFinished, Attempt: attempt, StepID: startStepID, StepType: spec.StepGoToURL, StepName: "Open start URL", DurationMs: time.Since(startAt).Milliseconds()}
	if navErr != nil {
		startEv.Status = spec.StatusFailed
		startEv.Error = fmt.Sprintf("could not open %s: %v", scope.Masker().Mask(startURL), navErr)
		startEv.Screenshot = screenshot(current, s, runDir, &screenshotIndex, false)
		emit.Emit(startEv)
		result.status = spec.StatusFailed
		result.failedStepID = startStepID
		result.err = startEv.Error
		result.resources, _ = session.Net.Write()
		return result
	}
	startEv.Status = spec.StatusPassed
	startEv.Screenshot = screenshot(current, s, runDir, &screenshotIndex, false)
	emit.Emit(startEv)

	for _, step := range s.Steps {
		if ctx.Err() != nil {
			result.status = spec.StatusError
			result.err = ctx.Err().Error()
			break
		}
		stepCtx.Page = current
		stepCtx.Timeout = DefaultStepTimeout
		if step.TimeoutSec > 0 {
			stepCtx.Timeout = time.Duration(step.TimeoutSec) * time.Second
		}
		session.Net.SetStep(step.ID)

		emit.Emit(spec.Event{Ev: spec.EvStepStarted, Attempt: attempt, StepID: step.ID, StepType: step.Type, StepName: step.Name})
		started := time.Now()
		outcome, err := steps.Run(stepCtx, step)
		// The step may have switched pages (popup); pick up the current one
		// for the screenshot and the next step.
		stepCtx.Page = current

		ev := spec.Event{
			Ev: spec.EvStepFinished, Attempt: attempt,
			StepID: step.ID, StepType: step.Type, StepName: step.Name,
			DurationMs:  time.Since(started).Milliseconds(),
			LocatorUsed: outcome.LocatorUsed,
			Extracted:   outcome.Extracted,
		}
		ev.Screenshot = screenshot(current, s, runDir, &screenshotIndex, step.NoScreenshot || outcome.SkipScreenshot)

		if err != nil {
			ev.Status = spec.StatusFailed
			ev.Error = err.Error()
			ev.AllowFailure = step.AllowFailure
			emit.Emit(ev)
			if step.AllowFailure {
				continue
			}
			result.status = spec.StatusFailed
			result.failedStepID = step.ID
			result.err = err.Error()
			break
		}
		ev.Status = spec.StatusPassed
		emit.Emit(ev)
	}

	resources, err := session.Net.Write()
	if err != nil {
		emit.Log("could not write %s: %v", spec.ResourcesFile, err)
	}
	result.resources = resources
	return result
}

// screenshot captures the page after a step, unless privacy says not to. A
// failed capture is not a failed step: the verdict was already decided.
func screenshot(page playwright.Page, s *spec.RunSpec, runDir string, index *int, skip bool) string {
	if s.Options.NoScreenshots || skip || page == nil {
		return ""
	}
	*index++
	name := spec.ScreenshotName(*index)
	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(filepath.Join(runDir, name)),
		FullPage: playwright.Bool(false),
		Timeout:  playwright.Float(10_000),
	}); err != nil {
		*index--
		return ""
	}
	return name
}

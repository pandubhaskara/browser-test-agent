// Package session holds a real browser open on the location VM and streams it
// to the editor: JPEG frames out on stdout, input events in on stdin, one
// NDJSON object per line over a single SSH exec.
//
// This is what makes the step editor's preview an actual browser rather than a
// rendered snapshot — real navigation, real JavaScript, real cookies, real
// logins — while nothing of the target site is ever served by Radar itself.
// Only pixels leave the VM.
//
// Element picking runs inside the live page: the backend ships the picker
// script with the start payload, it is injected on every document, and a pick
// evaluates it against the element actually under the cursor. That is more
// accurate than parsing a snapshot, because it uses the browser's own hit
// testing.
package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/browser"
	"browser-test-agent/internal/home"
	"browser-test-agent/internal/runner"
	"browser-test-agent/pkg/spec"
)

// Config is what the command line supplies.
type Config struct {
	Stdin     io.Reader
	Stdout    io.Writer
	DriverDir string
}

// navigateTimeout bounds one navigation, whether it came from the start URL,
// the address field or a click.
const navigateTimeout = 60 * time.Second

// Run drives the session until the editor closes it, it goes idle, it reaches
// its lifetime cap, or the transport drops. The exit code follows `run`: 0
// whenever the session itself worked, non-zero only when the runner could not
// start one.
func Run(ctx context.Context, cfg Config, start *spec.SessionStart) int {
	start.ApplyDefaults()
	if start.Spec != nil {
		// A session is a browser, not a test: no steps, no retries, no
		// artifacts.
		start.Spec.Steps = nil
		start.Spec.Retries = 0
		start.Spec.Options.CaptureHTTPPayloads = false
		start.Spec.Options.NoScreenshots = true
	}
	if err := start.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid session start: %v\n", err)
		return runner.ExitInfra
	}
	s := start.Spec

	out := &emitter{w: cfg.Stdout}
	scope := runner.NewScope(s)
	mask := scope.Masker().Mask
	if err := runner.ResolveLocals(scope, s); err != nil {
		out.error(mask(err.Error()))
		return runner.ExitOK
	}

	runDir, err := home.RunDir("session-" + s.RunID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runner.ExitInfra
	}
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "create session folder %s: %v\n", runDir, err)
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
		out.error(mask(err.Error()))
		return runner.ExitOK
	}
	defer launched.Close()

	sess, err := browser.NewContext(launched, s, runDir)
	if err != nil {
		var infra *browser.InfraError
		if errors.As(err, &infra) {
			fmt.Fprintln(os.Stderr, mask(err.Error()))
			return runner.ExitInfra
		}
		out.error(mask(err.Error()))
		return runner.ExitOK
	}
	defer sess.Close()
	sess.Net.Scrub = mask

	page := sess.Page

	// The picker has to survive every navigation, so it is an init script
	// rather than an injection after load.
	if start.PickerScript != "" {
		if err := page.AddInitScript(playwright.Script{Content: playwright.String(start.PickerScript)}); err != nil {
			out.error("could not install the element picker: " + err.Error())
			return runner.ExitOK
		}
	}

	// A navigation the session did not ask for — a redirect, a script setting
	// location — is where a non-http document could appear, so the guard lives
	// here as well as on every navigate request.
	badScheme := make(chan string, 1)
	page.On("framenavigated", func(frame playwright.Frame) {
		if frame != page.MainFrame() {
			return
		}
		url := frame.URL()
		if url == "" || url == "about:blank" {
			return
		}
		if err := spec.ValidHTTPURL(url); err != nil {
			select {
			case badScheme <- url:
			default:
			}
			return
		}
		title, _ := page.Title()
		out.event(spec.SessionEvent{
			Type: spec.SessionOutNav, URL: mask(url), Title: mask(title),
		})
	})

	startURL, _, err := scope.Resolve(s.StartURL)
	if err != nil {
		out.error(err.Error())
		return runner.ExitOK
	}
	if err := spec.ValidHTTPURL(startURL); err != nil {
		out.error(mask(err.Error()))
		return runner.ExitOK
	}
	if _, err := page.Goto(startURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(float64(navigateTimeout / time.Millisecond)),
		WaitUntil: playwright.WaitUntilStateLoad,
	}); err != nil {
		out.error(fmt.Sprintf("could not open %s: %v", mask(startURL), err))
		return runner.ExitOK
	}

	title, _ := page.Title()
	ready := spec.SessionEvent{Type: spec.SessionOutReady, URL: mask(page.URL()), Title: mask(title)}
	if vp := page.ViewportSize(); vp != nil {
		// The editor scales its clicks from the displayed image back to these,
		// so they are the session's coordinate system.
		ready.Width, ready.Height = vp.Width, vp.Height
	}
	out.event(ready)

	stopCast, err := startScreencast(sess.Context, page, start, out)
	if err != nil {
		// Without frames there is nothing to look at, but say so rather than
		// exiting silently: Firefox has no screencast, which is why the editor
		// keeps the snapshot preview as a fallback.
		out.error("this browser cannot stream a live view: " + err.Error())
		return runner.ExitOK
	}
	defer stopCast()

	return pump(ctx, cfg.Stdin, out, page, start, badScheme, mask)
}

// pump applies input until something ends the session.
func pump(ctx context.Context, stdin io.Reader, out *emitter, page playwright.Page, start *spec.SessionStart, badScheme <-chan string, mask func(string) string) int {
	inputs := make(chan spec.SessionInput)
	readErr := make(chan error, 1)
	go func() {
		defer close(inputs)
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			in, err := spec.DecodeSessionInput([]byte(line))
			if err != nil {
				out.error(err.Error())
				continue
			}
			inputs <- in
		}
		readErr <- sc.Err()
	}()

	idle := time.NewTimer(time.Duration(start.IdleSeconds) * time.Second)
	defer idle.Stop()
	lifetime := time.NewTimer(time.Duration(start.MaxSeconds) * time.Second)
	defer lifetime.Stop()

	for {
		select {
		case <-ctx.Done():
			out.closed("the session was cancelled")
			return runner.ExitOK

		case url := <-badScheme:
			// Already navigated, so the only safe move is to end the session.
			out.error(fmt.Sprintf("refusing to show %s: only http(s) pages can be previewed", mask(url)))
			out.closed("blocked navigation")
			return runner.ExitOK

		case <-idle.C:
			out.closed(fmt.Sprintf("closed after %ds with no input", start.IdleSeconds))
			return runner.ExitOK

		case <-lifetime.C:
			out.closed(fmt.Sprintf("closed after %ds; reopen the preview to continue", start.MaxSeconds))
			return runner.ExitOK

		case err := <-readErr:
			if err != nil {
				fmt.Fprintf(os.Stderr, "read session input: %v\n", err)
			}
			out.closed("the editor disconnected")
			return runner.ExitOK

		case in, ok := <-inputs:
			if !ok {
				out.closed("the editor disconnected")
				return runner.ExitOK
			}
			if !idle.Stop() {
				// Drained or already fired; either way it is being reset.
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(time.Duration(start.IdleSeconds) * time.Second)

			if in.Type == spec.SessionInClose {
				out.closed("closed by the editor")
				return runner.ExitOK
			}
			if err := in.Validate(); err != nil {
				out.error(err.Error())
				continue
			}
			if err := apply(page, in, out, mask); err != nil {
				out.error(mask(err.Error()))
			}
		}
	}
}

// apply performs one input against the live page.
func apply(page playwright.Page, in spec.SessionInput, out *emitter, mask func(string) string) error {
	switch in.Type {
	case spec.SessionInPing:
		return nil

	case spec.SessionInNavigate:
		_, err := page.Goto(in.URL, playwright.PageGotoOptions{
			Timeout:   playwright.Float(float64(navigateTimeout / time.Millisecond)),
			WaitUntil: playwright.WaitUntilStateLoad,
		})
		if err != nil {
			return fmt.Errorf("could not open %s: %w", in.URL, err)
		}
		return nil

	case spec.SessionInBack:
		_, err := page.GoBack(playwright.PageGoBackOptions{Timeout: playwright.Float(float64(navigateTimeout / time.Millisecond))})
		return err

	case spec.SessionInForward:
		_, err := page.GoForward(playwright.PageGoForwardOptions{Timeout: playwright.Float(float64(navigateTimeout / time.Millisecond))})
		return err

	case spec.SessionInReload:
		_, err := page.Reload(playwright.PageReloadOptions{Timeout: playwright.Float(float64(navigateTimeout / time.Millisecond))})
		return err

	case spec.SessionInMouse:
		return mouse(page, in)

	case spec.SessionInKey:
		return page.Keyboard().Press(in.Key)

	case spec.SessionInText:
		return page.Keyboard().InsertText(in.Text)

	case spec.SessionInViewport:
		return page.SetViewportSize(in.Width, in.Height)

	case spec.SessionInPick:
		return pick(page, in, out, mask)
	}
	return fmt.Errorf("unknown input type %q", in.Type)
}

func mouse(page playwright.Page, in spec.SessionInput) error {
	m := page.Mouse()
	button := mouseButton(in.Button)
	switch in.Action {
	case spec.MouseMove:
		return m.Move(in.X, in.Y)
	case spec.MouseDown:
		return m.Down(playwright.MouseDownOptions{Button: button})
	case spec.MouseUp:
		return m.Up(playwright.MouseUpOptions{Button: button})
	case spec.MouseClick:
		return m.Click(in.X, in.Y, playwright.MouseClickOptions{Button: button})
	case spec.MouseDblclick:
		return m.Dblclick(in.X, in.Y, playwright.MouseDblclickOptions{Button: button})
	case spec.MouseWheel:
		return m.Wheel(in.DeltaX, in.DeltaY)
	}
	return fmt.Errorf("unknown mouse action %q", in.Action)
}

func mouseButton(name string) *playwright.MouseButton {
	switch name {
	case "middle":
		return playwright.MouseButtonMiddle
	case "right":
		return playwright.MouseButtonRight
	default:
		return playwright.MouseButtonLeft
	}
}

// pick asks the injected picker for the locator strategies of whatever is at
// the coordinate, in the page the user is actually looking at.
func pick(page playwright.Page, in spec.SessionInput, out *emitter, mask func(string) string) error {
	const expr = `([x, y]) => {
		if (!window.__radarBtPicker) return null;
		var el = document.elementFromPoint(x, y);
		if (!el) return null;
		return window.__radarBtPicker.locatorFor(el);
	}`
	raw, err := page.Evaluate(expr, []any{in.X, in.Y})
	if err != nil {
		return fmt.Errorf("could not read the element: %w", err)
	}
	if raw == nil {
		return errors.New("no element at that point")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("could not read the element: %w", err)
	}
	var el spec.Element
	if err := json.Unmarshal(encoded, &el); err != nil {
		return fmt.Errorf("could not read the element: %w", err)
	}
	el.Description = mask(el.Description)
	el.Text = mask(el.Text)
	out.event(spec.SessionEvent{Type: spec.SessionOutPicked, Element: &el})
	return nil
}

// startScreencast streams the page as JPEG frames. Each frame is acked only
// after it has been handed to the writer, which is what keeps a slow consumer
// from queueing frames on the VM.
func startScreencast(ctx playwright.BrowserContext, page playwright.Page, start *spec.SessionStart, out *emitter) (func(), error) {
	cdp, err := ctx.NewCDPSession(page)
	if err != nil {
		return nil, err
	}

	cdp.On("Page.screencastFrame", func(payload any) {
		params, ok := payload.(map[string]any)
		if !ok {
			return
		}
		data, _ := params["data"].(string)
		sessionID, hasID := params["sessionId"]
		if data != "" && len(data) <= spec.SessionMaxFrameB64 {
			ev := spec.SessionEvent{Type: spec.SessionOutFrame, B64: data}
			if meta, ok := params["metadata"].(map[string]any); ok {
				if w, ok := meta["deviceWidth"].(float64); ok {
					ev.Width = int(w)
				}
				if h, ok := meta["deviceHeight"].(float64); ok {
					ev.Height = int(h)
				}
			}
			out.event(ev)
		}
		if hasID {
			// Unacked frames stop the stream, so ack even a frame that was
			// dropped for being oversized.
			_, _ = cdp.Send("Page.screencastFrameAck", map[string]any{"sessionId": sessionID})
		}
	})

	if _, err := cdp.Send("Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       start.Quality,
		"maxWidth":      start.MaxWidth,
		"maxHeight":     start.MaxHeight,
		"everyNthFrame": 1,
	}); err != nil {
		return nil, err
	}

	return func() {
		_, _ = cdp.Send("Page.stopScreencast", nil)
		_ = cdp.Detach()
	}, nil
}

// emitter serialises stdout: frames arrive from the screencast callback while
// events come from the input loop, and a half-written line would break the
// reader on the other end.
type emitter struct {
	mu sync.Mutex
	w  io.Writer
}

func (e *emitter) event(ev spec.SessionEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = spec.EncodeSessionEvent(e.w, ev)
}

func (e *emitter) error(msg string) {
	e.event(spec.SessionEvent{Type: spec.SessionOutError, Message: msg})
}

func (e *emitter) closed(msg string) {
	e.event(spec.SessionEvent{Type: spec.SessionOutClosed, Message: msg})
}

package steps

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/locator"
	"browser-test-agent/pkg/spec"
)

func goToURL(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.GoToURLParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	if _, err := c.Page.Goto(p.URL, playwright.PageGotoOptions{
		Timeout:   c.ms(),
		WaitUntil: playwright.WaitUntilStateLoad,
	}); err != nil {
		return Outcome{}, fmt.Errorf("could not open %s: %w", p.URL, err)
	}
	return Outcome{}, nil
}

func refresh(c *Context, _ spec.Step, _ json.RawMessage) (Outcome, error) {
	if _, err := c.Page.Reload(playwright.PageReloadOptions{
		Timeout:   c.ms(),
		WaitUntil: playwright.WaitUntilStateLoad,
	}); err != nil {
		return Outcome{}, fmt.Errorf("reload failed: %w", err)
	}
	return Outcome{}, nil
}

// wait pauses. It is the one step that sleeps on purpose: the user asked for
// exactly this, usually to let an animation or a polling widget settle. The
// run's context still cancels it.
func wait(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.WaitParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	if p.Seconds > spec.MaxWaitSeconds {
		p.Seconds = spec.MaxWaitSeconds
	}
	select {
	case <-time.After(time.Duration(p.Seconds) * time.Second):
		return Outcome{}, nil
	case <-c.Ctx.Done():
		return Outcome{}, c.Ctx.Err()
	}
}

func scroll(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.ScrollParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	if p.Element != nil {
		loc, used, err := locator.Find(c.Ctx, c.Page, *p.Element, c.Timeout)
		if err != nil {
			return Outcome{}, err
		}
		if err := loc.ScrollIntoViewIfNeeded(playwright.LocatorScrollIntoViewIfNeededOptions{Timeout: c.ms()}); err != nil {
			return Outcome{LocatorUsed: used}, fmt.Errorf("could not scroll to %s: %w", locator.Describe(*p.Element), err)
		}
		return Outcome{LocatorUsed: used}, nil
	}
	if err := c.Page.Mouse().Wheel(float64(p.X), float64(p.Y)); err != nil {
		return Outcome{}, fmt.Errorf("scroll by (%d, %d) failed: %w", p.X, p.Y, err)
	}
	return Outcome{}, nil
}

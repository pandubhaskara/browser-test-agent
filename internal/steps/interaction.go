package steps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/locator"
	"browser-test-agent/pkg/spec"
)

func click(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.ClickParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	opts := playwright.LocatorClickOptions{Timeout: c.ms()}
	switch p.Button {
	case "right":
		opts.Button = playwright.MouseButtonRight
	case "middle":
		opts.Button = playwright.MouseButtonMiddle
	}
	if p.Double {
		opts.ClickCount = playwright.Int(2)
	}
	if err := loc.Click(opts); err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not click %s: %w", locator.Describe(p.Element), err)
	}
	return Outcome{LocatorUsed: used}, nil
}

func hover(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.HoverParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	if err := loc.Hover(playwright.LocatorHoverOptions{Timeout: c.ms()}); err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not hover %s: %w", locator.Describe(p.Element), err)
	}
	return Outcome{LocatorUsed: used}, nil
}

// typeText fills a field. Fill replaces the value and fires one input event,
// which is what "type text" means for a form; PressSequentially is used only
// when the author asked to append, because it drives autocomplete widgets that
// react per keystroke.
func typeText(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.TypeTextParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	clear := p.Clear == nil || *p.Clear
	if clear {
		if err := loc.Fill(p.Text, playwright.LocatorFillOptions{Timeout: c.ms()}); err != nil {
			return Outcome{LocatorUsed: used}, fmt.Errorf("could not type into %s: %w", locator.Describe(p.Element), err)
		}
	} else if err := loc.PressSequentially(p.Text, playwright.LocatorPressSequentiallyOptions{Timeout: c.ms()}); err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not type into %s: %w", locator.Describe(p.Element), err)
	}
	return Outcome{LocatorUsed: used}, nil
}

// pressKey sends a key with modifiers, e.g. Shift+Enter, to the focused element.
func pressKey(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.PressKeyParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	combo := strings.Join(append(append([]string{}, p.Modifiers...), p.Key), "+")
	if err := c.Page.Keyboard().Press(combo); err != nil {
		return Outcome{}, fmt.Errorf("could not press %s: %w", combo, err)
	}
	return Outcome{}, nil
}

// selectOption picks by value or visible label, whichever matches: an author
// copying what they see in the dropdown should not have to know which one the
// markup uses.
func selectOption(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.SelectOptionParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	values := []string{p.Value}
	if _, err := loc.SelectOption(playwright.SelectOptionValues{ValuesOrLabels: &values}, playwright.LocatorSelectOptionOptions{Timeout: c.ms()}); err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not select %q in %s: %w", p.Value, locator.Describe(p.Element), err)
	}
	return Outcome{LocatorUsed: used}, nil
}

// uploadFiles feeds files the worker shipped into runs/<id>/uploads/ to a file
// input. Paths are confined to that folder: a step param is user input, and
// "../../etc/passwd" must read as a missing file, not as a file.
func uploadFiles(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.UploadFilesParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	base := filepath.Join(c.RunDir, spec.UploadsDir)
	paths := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		full := filepath.Join(base, filepath.FromSlash(f.Path))
		rel, relErr := filepath.Rel(base, full)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return Outcome{LocatorUsed: used}, fmt.Errorf("upload file %q is outside the run's uploads folder", f.Path)
		}
		if _, err := os.Stat(full); err != nil {
			return Outcome{LocatorUsed: used}, fmt.Errorf("upload file %q was not shipped to the VM: %w", f.Name, err)
		}
		paths = append(paths, full)
	}
	if err := loc.SetInputFiles(paths, playwright.LocatorSetInputFilesOptions{Timeout: c.ms()}); err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not attach files to %s: %w", locator.Describe(p.Element), err)
	}
	return Outcome{LocatorUsed: used}, nil
}

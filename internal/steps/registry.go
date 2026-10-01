// Package steps executes one authored step against a live page. The registry
// maps a step type to its executor; everything a step needs — the page, the
// variable scope, the network log, the timeout — arrives in a Context so an
// executor is a plain function that is easy to test in isolation.
//
// A step fails by returning an error. The message is the step's error in the
// result, so it is written for the person reading a failed run ("Checkout
// button not found (tried …)"), not for a developer with the code open.
package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/browser"
	"browser-test-agent/pkg/spec"
	"browser-test-agent/pkg/vars"
)

// Context is everything an executor may touch.
type Context struct {
	Ctx     context.Context
	Page    playwright.Page
	Browser playwright.BrowserContext
	Scope   *vars.Scope
	Net     *browser.NetworkLog
	// RunDir is runs/<run_id>, where uploads were shipped to.
	RunDir string
	// Timeout is this step's budget; every Playwright call gets it.
	Timeout time.Duration
	Log     func(format string, args ...any)
}

// ms is the timeout as Playwright wants it.
func (c *Context) ms() *float64 {
	return playwright.Float(float64(c.Timeout / time.Millisecond))
}

// Outcome is what an executor reports beyond pass/fail.
type Outcome struct {
	// LocatorUsed is which Element strategy matched, for brittle-locator visibility.
	LocatorUsed string
	// Extracted is a variable the step bound.
	Extracted *spec.Extracted
	// SkipScreenshot is set when the step typed a secure value: the page may
	// now show it in a field.
	SkipScreenshot bool
}

// Executor runs one step whose params have already had variables resolved.
type Executor func(c *Context, step spec.Step, params json.RawMessage) (Outcome, error)

var registry = map[string]Executor{
	spec.StepGoToURL:                goToURL,
	spec.StepRefresh:                refresh,
	spec.StepWait:                   wait,
	spec.StepScroll:                 scroll,
	spec.StepClick:                  click,
	spec.StepHover:                  hover,
	spec.StepTypeText:               typeText,
	spec.StepPressKey:               pressKey,
	spec.StepSelectOption:           selectOption,
	spec.StepAssertCurrentURL:       assertCurrentURL,
	spec.StepAssertPageContains:     assertPageContains,
	spec.StepAssertPageLacks:        assertPageLacks,
	spec.StepAssertElementPresent:   assertElementPresent,
	spec.StepAssertElementContent:   assertElementContent,
	spec.StepAssertElementAttribute: assertElementAttribute,
	spec.StepAssertCheckboxState:    assertCheckboxState,
	spec.StepExtractVariable:        extractVariable,
	spec.StepAssertFromJS:           assertFromJS,
	spec.StepExtractFromJS:          extractFromJS,
	spec.StepAssertFileDownload:     assertFileDownload,
	spec.StepUploadFiles:            uploadFiles,
	spec.StepAssertRequests:         assertRequests,
	spec.StepRunAPITest:             runAPITest,
}

// Implemented lists the step types this runner executes, for `doctor`.
func Implemented() []string {
	out := make([]string, 0, len(registry))
	for _, t := range spec.StepTypes {
		if _, ok := registry[t]; ok {
			out = append(out, t)
		}
	}
	return out
}

// Run resolves the step's variable references and dispatches it. Resolution
// happens here, once, for every step type: a `{{ }}` in any string parameter
// is replaced, and a secure value anywhere in the params suppresses the
// screenshot.
func Run(c *Context, step spec.Step) (Outcome, error) {
	exec, ok := registry[step.Type]
	if !ok {
		return Outcome{}, fmt.Errorf("step type %q is not implemented by this runner", step.Type)
	}

	params, usedSecure, err := resolveParams(c.Scope, step.Params)
	if err != nil {
		return Outcome{}, err
	}

	out, err := exec(c, step, params)
	if usedSecure {
		out.SkipScreenshot = true
	}
	return out, err
}

// resolveParams walks the params JSON and resolves every string. Doing it on
// the generic shape rather than per type means a new step type gets variable
// support without remembering to add it.
func resolveParams(scope *vars.Scope, raw json.RawMessage) (json.RawMessage, bool, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), false, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, fmt.Errorf("invalid step parameters: %w", err)
	}
	usedSecure := false
	var walk func(any) (any, error)
	walk = func(x any) (any, error) {
		switch t := x.(type) {
		case string:
			if !strings.Contains(t, "{{") {
				return t, nil
			}
			out, secure, err := scope.Resolve(t)
			if err != nil {
				return nil, err
			}
			if secure {
				usedSecure = true
			}
			return out, nil
		case map[string]any:
			for k, val := range t {
				r, err := walk(val)
				if err != nil {
					return nil, err
				}
				t[k] = r
			}
			return t, nil
		case []any:
			for i, val := range t {
				r, err := walk(val)
				if err != nil {
					return nil, err
				}
				t[i] = r
			}
			return t, nil
		}
		return x, nil
	}
	resolved, err := walk(v)
	if err != nil {
		return nil, false, err
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		return nil, false, err
	}
	return out, usedSecure, nil
}

func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid step parameters: %w", err)
	}
	return nil
}

// compare applies a spec comparison operator to text.
func compare(op, actual, expected string) (bool, error) {
	switch op {
	case spec.OpIs:
		return actual == expected, nil
	case spec.OpIsNot:
		return actual != expected, nil
	case spec.OpContains:
		return strings.Contains(actual, expected), nil
	case spec.OpNotContains:
		return !strings.Contains(actual, expected), nil
	case spec.OpStartsWith:
		return strings.HasPrefix(actual, expected), nil
	case spec.OpEndsWith:
		return strings.HasSuffix(actual, expected), nil
	case spec.OpMatches, spec.OpNotMatches:
		re, err := regexp.Compile(expected)
		if err != nil {
			return false, fmt.Errorf("pattern %q is not a valid regular expression: %w", expected, err)
		}
		m := re.MatchString(actual)
		if op == spec.OpNotMatches {
			m = !m
		}
		return m, nil
	}
	return false, fmt.Errorf("unknown comparison %q", op)
}

// opLabel spells an operator for an error message.
func opLabel(op string) string {
	return strings.ReplaceAll(op, "_", " ")
}

// excerpt trims long page text in error messages.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

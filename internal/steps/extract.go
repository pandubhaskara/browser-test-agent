package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/locator"
	"browser-test-agent/pkg/spec"
	"browser-test-agent/pkg/vars"
)

// extractVariable binds an element's text to a run-scoped variable for later
// steps. A secure extraction registers the value with the masker before
// anything can print it.
func extractVariable(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.ExtractVariableParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	text, err := loc.InnerText(playwright.LocatorInnerTextOptions{Timeout: c.ms()})
	if err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not read %s: %w", locator.Describe(p.Element), err)
	}
	value := strings.TrimSpace(text)
	c.Scope.Set(p.Name, value, p.Secure)
	return Outcome{LocatorUsed: used, Extracted: extracted(p.Name, value, p.Secure)}, nil
}

// extracted builds the event field, masked when secure. The masker will scrub
// it again on output; masking here keeps the raw value out of memory that a
// later log line could reach.
func extracted(name, value string, secure bool) *spec.Extracted {
	if secure {
		value = vars.Mask
	}
	return &spec.Extracted{Name: name, Value: value, Secure: secure}
}

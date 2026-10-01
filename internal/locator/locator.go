// Package locator turns a spec.Element — several lookup strategies stored
// together — into the one Playwright locator that matches right now.
//
// No single strategy survives every front-end change: a data-testid is removed
// in a refactor, a CSS path breaks on a restyle, visible text is translated.
// Storing several and trying them in order is what keeps a test from failing
// for a reason that has nothing to do with the site being down. Which one
// matched is reported on the step result, so a test that has quietly fallen
// back to its last strategy is visible before it fails outright.
package locator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/pkg/spec"
)

// Strategy names, as reported in step_finished.locator_used.
const (
	StrategyTestID = "test_id"
	StrategyCSS    = "css"
	StrategyXPath  = "xpath"
	StrategyText   = "text"
)

// pollInterval is how often the strategies are retried while waiting for the
// element to appear. Fast enough to feel instant, slow enough not to hammer
// the browser's protocol channel.
const pollInterval = 200 * time.Millisecond

// candidate is one strategy's selector.
type candidate struct {
	strategy string
	selector string
}

// candidates lists the element's strategies in priority order.
func candidates(el spec.Element) []candidate {
	var out []candidate
	if el.TestID != "" {
		out = append(out, candidate{StrategyTestID, fmt.Sprintf(`[data-testid=%q]`, el.TestID)})
	}
	if el.CSS != "" {
		out = append(out, candidate{StrategyCSS, el.CSS})
	}
	if el.XPath != "" {
		out = append(out, candidate{StrategyXPath, "xpath=" + el.XPath})
	}
	if el.Text != "" {
		// Exact visible-text match; Playwright's text engine is case-sensitive
		// and whitespace-normalising when quoted.
		out = append(out, candidate{StrategyText, fmt.Sprintf(`text=%q`, el.Text)})
	}
	return out
}

// Find resolves el on page, waiting up to timeout for any strategy to match.
// It returns the first matching element (Playwright's `.First()`), the
// strategy that matched, and a message naming every strategy tried when none
// did — that message is the step's error, so it says which selectors were
// looked for rather than just "not found".
func Find(ctx context.Context, page playwright.Page, el spec.Element, timeout time.Duration) (playwright.Locator, string, error) {
	cands := candidates(el)
	if len(cands) == 0 {
		return nil, "", errors.New("element has no locator")
	}

	deadline := time.Now().Add(timeout)
	for {
		for _, c := range cands {
			loc := page.Locator(c.selector)
			n, err := loc.Count()
			if err != nil {
				// A malformed selector fails Count; try the next strategy
				// rather than giving up on the element.
				continue
			}
			if n > 0 {
				return loc.First(), c.strategy, nil
			}
		}

		if time.Now().After(deadline) {
			return nil, "", notFound(el, cands)
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func notFound(el spec.Element, cands []candidate) error {
	tried := make([]string, 0, len(cands))
	for _, c := range cands {
		tried = append(tried, c.strategy+"="+c.selector)
	}
	what := el.Description
	if what == "" {
		what = "element"
	}
	return fmt.Errorf("%s not found (tried %s)", what, strings.Join(tried, ", "))
}

// Describe is the element's label for messages: its description, else its
// first strategy.
func Describe(el spec.Element) string {
	if el.Description != "" {
		return el.Description
	}
	if c := candidates(el); len(c) > 0 {
		return c[0].selector
	}
	return "element"
}

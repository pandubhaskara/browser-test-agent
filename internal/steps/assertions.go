package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/internal/browser"
	"browser-test-agent/internal/locator"
	"browser-test-agent/pkg/spec"
)

func assertCurrentURL(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertCurrentURLParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	actual := c.Page.URL()
	ok, err := compare(p.Op, actual, p.Value)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("current URL is %q, expected it to %s %q", actual, opLabel(p.Op), p.Value)
	}
	return Outcome{}, nil
}

// pageText is the visible text of the page, which is what a user means by "the
// page contains". innerText, not textContent: hidden elements and scripts are
// not on the page as far as a reader is concerned.
func pageText(c *Context) (string, error) {
	text, err := c.Page.InnerText("body", playwright.PageInnerTextOptions{Timeout: c.ms()})
	if err != nil {
		return "", fmt.Errorf("could not read the page text: %w", err)
	}
	return text, nil
}

func assertPageContains(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertPageTextParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	// The text may arrive a moment after load; poll within the step budget
	// rather than fail on the first read.
	var last string
	err := pollUntil(c, func() (bool, error) {
		text, err := pageText(c)
		if err != nil {
			return false, err
		}
		last = text
		return strings.Contains(text, p.Text), nil
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("page does not contain %q (page text: %s)", p.Text, excerpt(last))
	}
	return Outcome{}, nil
}

func assertPageLacks(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertPageTextParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	text, err := pageText(c)
	if err != nil {
		return Outcome{}, err
	}
	if strings.Contains(text, p.Text) {
		return Outcome{}, fmt.Errorf("page contains %q but should not", p.Text)
	}
	return Outcome{}, nil
}

func assertElementPresent(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertElementPresentParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	if err := loc.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: c.ms(),
	}); err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("%s is in the page but not visible: %w", locator.Describe(p.Element), err)
	}
	return Outcome{LocatorUsed: used}, nil
}

func assertElementContent(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertElementContentParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	var last string
	pollErr := pollUntil(c, func() (bool, error) {
		text, err := loc.InnerText(playwright.LocatorInnerTextOptions{Timeout: c.ms()})
		if err != nil {
			return false, fmt.Errorf("could not read %s: %w", locator.Describe(p.Element), err)
		}
		last = strings.TrimSpace(text)
		return compare(p.Op, last, p.Value)
	})
	if pollErr != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("%s reads %q, expected it to %s %q", locator.Describe(p.Element), excerpt(last), opLabel(p.Op), p.Value)
	}
	return Outcome{LocatorUsed: used}, nil
}

func assertElementAttribute(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertElementAttributeParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	actual, err := loc.GetAttribute(p.Attr, playwright.LocatorGetAttributeOptions{Timeout: c.ms()})
	if err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not read attribute %s of %s: %w", p.Attr, locator.Describe(p.Element), err)
	}
	ok, err := compare(p.Op, actual, p.Value)
	if err != nil {
		return Outcome{LocatorUsed: used}, err
	}
	if !ok {
		return Outcome{LocatorUsed: used}, fmt.Errorf("%s has %s=%q, expected it to %s %q", locator.Describe(p.Element), p.Attr, actual, opLabel(p.Op), p.Value)
	}
	return Outcome{LocatorUsed: used}, nil
}

func assertCheckboxState(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertCheckboxStateParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	loc, used, err := locator.Find(c.Ctx, c.Page, p.Element, c.Timeout)
	if err != nil {
		return Outcome{}, err
	}
	checked, err := loc.IsChecked(playwright.LocatorIsCheckedOptions{Timeout: c.ms()})
	if err != nil {
		return Outcome{LocatorUsed: used}, fmt.Errorf("could not read the checked state of %s: %w", locator.Describe(p.Element), err)
	}
	if checked != p.Checked {
		want := "checked"
		if !p.Checked {
			want = "unchecked"
		}
		return Outcome{LocatorUsed: used}, fmt.Errorf("%s is not %s", locator.Describe(p.Element), want)
	}
	return Outcome{LocatorUsed: used}, nil
}

// assertRequests looks through the network log for a matching request. The
// pattern is a Chrome match pattern or a plain substring, the same forms
// blocked_requests accepts.
func assertRequests(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertRequestsParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	re, err := browser.MatchPattern(p.URLPattern)
	if err != nil {
		return Outcome{}, fmt.Errorf("url_pattern %q: %w", p.URLPattern, err)
	}
	want := p.MinCount
	if want <= 0 {
		want = 1
	}
	var count int
	pollErr := pollUntil(c, func() (bool, error) {
		count = 0
		for _, r := range c.Net.Resources() {
			if !re.MatchString(r.URL) {
				continue
			}
			if p.Method != "" && !strings.EqualFold(p.Method, r.Method) {
				continue
			}
			if p.Status != 0 && r.Status != p.Status {
				continue
			}
			count++
		}
		return count >= want, nil
	})
	if pollErr != nil {
		desc := p.URLPattern
		if p.Method != "" {
			desc = p.Method + " " + desc
		}
		if p.Status != 0 {
			desc += fmt.Sprintf(" → %d", p.Status)
		}
		return Outcome{}, fmt.Errorf("expected at least %d request(s) matching %s, saw %d", want, desc, count)
	}
	return Outcome{}, nil
}

// assertFileDownload checks that a download happened during the run so far,
// optionally with a given name fragment, size and MD5.
func assertFileDownload(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertFileDownloadParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	var seen []string
	pollErr := pollUntil(c, func() (bool, error) {
		seen = seen[:0]
		for _, d := range c.Net.Downloads() {
			seen = append(seen, d.SuggestedName)
			if d.Failed != "" {
				continue
			}
			if p.Name != "" && !strings.Contains(d.SuggestedName, p.Name) {
				continue
			}
			if p.Size != 0 && d.Size != p.Size {
				continue
			}
			if p.MD5 != "" && !strings.EqualFold(d.MD5, p.MD5) {
				continue
			}
			return true, nil
		}
		return false, nil
	})
	if pollErr != nil {
		var want []string
		if p.Name != "" {
			want = append(want, "name containing "+fmt.Sprintf("%q", p.Name))
		}
		if p.Size != 0 {
			want = append(want, fmt.Sprintf("size %d", p.Size))
		}
		if p.MD5 != "" {
			want = append(want, "md5 "+p.MD5)
		}
		if len(want) == 0 {
			want = append(want, "any file")
		}
		return Outcome{}, fmt.Errorf("no download matched (%s); downloads seen: %v", strings.Join(want, ", "), seen)
	}
	return Outcome{}, nil
}

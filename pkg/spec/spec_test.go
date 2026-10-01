package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

func step(id, typ string, params any) Step {
	raw, _ := json.Marshal(params)
	return Step{ID: id, Type: typ, Name: id, Params: raw}
}

func validSpec() RunSpec {
	return RunSpec{
		RunID:    "r1",
		TestID:   "t1",
		StartURL: "https://example.com",
		Browser:  BrowserChrome,
		Device:   DeviceLaptopLarge,
		Steps: []Step{
			step("s1", StepClick, ClickParams{Element: Element{CSS: "#go"}}),
			step("s2", StepAssertPageContains, AssertPageTextParams{Text: "Welcome"}),
		},
	}
}

func TestValidSpecPasses(t *testing.T) {
	s := validSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	s := validSpec()
	s.Browser = "safari"
	s.Device = "watch"
	s.Steps = append(s.Steps,
		step("s3", StepWait, WaitParams{Seconds: 0}),
		step("s3", "teleport", nil),
		step("s5", StepClick, ClickParams{}),
	)
	err := s.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{`browser "safari"`, `device "watch"`, "seconds must be between", "duplicate id", `unknown step type "teleport"`, "element needs at least one locator"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
}

func TestValidateParamsPerType(t *testing.T) {
	good := map[string]any{
		StepGoToURL:                GoToURLParams{URL: "https://x"},
		StepRefresh:                nil,
		StepWait:                   WaitParams{Seconds: 3},
		StepScroll:                 ScrollParams{X: 0, Y: 500},
		StepClick:                  ClickParams{Element: Element{TestID: "buy"}, Button: "right", Double: true},
		StepHover:                  HoverParams{Element: Element{Text: "Menu"}},
		StepTypeText:               TypeTextParams{Element: Element{CSS: "#q"}, Text: "hi"},
		StepPressKey:               PressKeyParams{Key: "Enter", Modifiers: []string{"Shift"}},
		StepSelectOption:           SelectOptionParams{Element: Element{CSS: "select"}, Value: "b"},
		StepAssertCurrentURL:       AssertCurrentURLParams{Op: OpContains, Value: "/done"},
		StepAssertPageContains:     AssertPageTextParams{Text: "ok"},
		StepAssertPageLacks:        AssertPageTextParams{Text: "error"},
		StepAssertElementPresent:   AssertElementPresentParams{Element: Element{XPath: "//h1"}},
		StepAssertElementContent:   AssertElementContentParams{Element: Element{CSS: "h1"}, Op: OpIs, Value: "Hi"},
		StepAssertElementAttribute: AssertElementAttributeParams{Element: Element{CSS: "a"}, Attr: "href", Op: OpStartsWith, Value: "https"},
		StepAssertCheckboxState:    AssertCheckboxStateParams{Element: Element{CSS: "#c"}, Checked: true},
		StepExtractVariable:        ExtractVariableParams{Element: Element{CSS: "#id"}, Name: "ORDER_ID"},
		StepAssertFromJS:           AssertFromJSParams{Code: "return true"},
		StepExtractFromJS:          ExtractFromJSParams{Code: "document.title", Name: "TITLE"},
		StepAssertFileDownload:     AssertFileDownloadParams{Name: "report"},
		StepUploadFiles:            UploadFilesParams{Element: Element{CSS: "input[type=file]"}, Files: []FileRef{{Name: "a.pdf", Path: "a.pdf"}}},
		StepAssertRequests:         AssertRequestsParams{URLPattern: "*://api.example.com/*", Status: 200},
		StepRunAPITest:             RunAPITestParams{Method: "GET", URL: "https://api", Assertions: []APIAssertion{{Target: "status_code", Op: OpIs, Value: "200"}}},
	}
	for typ, p := range good {
		raw, _ := json.Marshal(p)
		if err := ValidateParams(typ, raw); err != nil {
			t.Errorf("%s: unexpected error %v", typ, err)
		}
	}

	bad := map[string]any{
		StepGoToURL:            GoToURLParams{},
		StepWait:               WaitParams{Seconds: 301},
		StepClick:              ClickParams{Element: Element{CSS: "#x"}, Button: "side"},
		StepPressKey:           PressKeyParams{Key: "a", Modifiers: []string{"Hyper"}},
		StepAssertCurrentURL:   AssertCurrentURLParams{Op: "equals"},
		StepExtractVariable:    ExtractVariableParams{Element: Element{CSS: "#x"}, Name: "lower"},
		StepExtractFromJS:      ExtractFromJSParams{Code: "1", Name: "1ABC"},
		StepAssertFileDownload: AssertFileDownloadParams{MD5: "short"},
		StepUploadFiles:        UploadFilesParams{Element: Element{CSS: "i"}},
		StepAssertRequests:     AssertRequestsParams{},
		StepRunAPITest:         RunAPITestParams{URL: "https://x", Method: "FETCH"},
	}
	for typ, p := range bad {
		raw, _ := json.Marshal(p)
		if err := ValidateParams(typ, raw); err == nil {
			t.Errorf("%s: expected an error for %+v", typ, p)
		}
	}
}

func TestOptionsValidate(t *testing.T) {
	o := Options{HTTPAuth: &HTTPAuth{Type: "kerberos"}, Cookies: []string{"nonsense"}, StartURLTimeoutSec: 999}
	err := o.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"http_auth.type", "Set-Cookie syntax", "start_url_timeout_sec"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestScreenshotName(t *testing.T) {
	if got := ScreenshotName(3); got != "step-003.png" {
		t.Errorf("got %q", got)
	}
	if got := ScreenshotName(1234); got != "step-1234.png" {
		t.Errorf("got %q", got)
	}
}

func TestDevices(t *testing.T) {
	if vp := DeviceViewports[DeviceMobileSmall]; vp.Width != 320 || !vp.Mobile {
		t.Errorf("mobile_small = %+v", vp)
	}
	if vp := DeviceViewports[DeviceLaptopLarge]; vp.Mobile {
		t.Error("laptop_large must not be a mobile viewport")
	}
}

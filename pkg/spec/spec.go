// Package spec is the contract between the backend worker and the VM runner:
// the RunSpec the worker sends on stdin, the step vocabulary inside it, and
// the events the runner streams back on stdout.
//
// It is a separate, dependency-free package on purpose. The backend imports it
// (the worker builds RunSpecs and decodes events; the API validates steps as
// they are saved), and the runner imports it — so there is one definition of
// every type and the two sides cannot drift. Nothing in here touches a browser.
package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Browsers the runner can launch. "chrome" and "edge" are Chromium channels;
// "firefox" is Playwright's own Firefox build.
const (
	BrowserChrome  = "chrome"
	BrowserFirefox = "firefox"
	BrowserEdge    = "edge"
)

// Browsers lists every supported browser, in the order the UI offers them.
var Browsers = []string{BrowserChrome, BrowserFirefox, BrowserEdge}

// ValidBrowser reports whether name is a browser the runner knows.
func ValidBrowser(name string) bool {
	switch name {
	case BrowserChrome, BrowserFirefox, BrowserEdge:
		return true
	}
	return false
}

// Devices are the viewport presets, named as Datadog names them.
const (
	DeviceLaptopLarge = "laptop_large"
	DeviceTablet      = "tablet"
	DeviceMobileSmall = "mobile_small"
)

// Viewport is a device's screen size in CSS pixels.
type Viewport struct {
	Width  int  `json:"width"`
	Height int  `json:"height"`
	Mobile bool `json:"mobile"`
}

// DeviceViewports maps each device preset to its viewport. Mobile presets also
// enable touch and the mobile meta-viewport, which is what makes a responsive
// site render its phone layout rather than a shrunken desktop one.
var DeviceViewports = map[string]Viewport{
	DeviceLaptopLarge: {Width: 1440, Height: 1100},
	DeviceTablet:      {Width: 768, Height: 1020, Mobile: true},
	DeviceMobileSmall: {Width: 320, Height: 550, Mobile: true},
}

// Devices lists every device preset, in the order the UI offers them.
var Devices = []string{DeviceLaptopLarge, DeviceTablet, DeviceMobileSmall}

// ValidDevice reports whether name is a known preset.
func ValidDevice(name string) bool {
	_, ok := DeviceViewports[name]
	return ok
}

// Frequency bounds, in seconds: one minute to one week, as Datadog allows.
const (
	MinFrequencySec = 60
	MaxFrequencySec = 604800
)

// RunSpec is everything one run needs, sent to `vm-runner run` on stdin.
//
// It travels only over SSH stdin: never in command-line arguments (visible in
// the VM's process list) and never written to disk on the VM, because it can
// carry secure variables and a client certificate's private key.
type RunSpec struct {
	RunID    string `json:"run_id"`
	TestID   string `json:"test_id"`
	StartURL string `json:"start_url"`
	Browser  string `json:"browser"`
	Device   string `json:"device"`

	Steps   []Step  `json:"steps"`
	Options Options `json:"options"`

	// LocalVars are the test's own variables; values may use builtins such as
	// `{{ numeric(6) }}` and are resolved by the runner at run start.
	LocalVars []Variable `json:"local_vars"`
	// GlobalVars are already resolved by the backend (decrypted where secure).
	GlobalVars map[string]Value `json:"global_vars"`

	// Retries is how many times a failed attempt is repeated with a fresh
	// browser context before the run is final; RetryWaitMs is the pause between.
	Retries     int `json:"retries"`
	RetryWaitMs int `json:"retry_wait_ms"`

	// ClientCert is the PEM material for Options.ClientCert, resolved by the
	// backend from its encrypted store. Never logged.
	ClientCert *PEMPair `json:"client_cert,omitempty"`
}

// Value is a resolved variable. Secure values are masked in every event, error
// message and stored step parameter, and a step that types one takes no
// screenshot.
type Value struct {
	V      string `json:"v"`
	Secure bool   `json:"secure"`
}

// Variable is a local variable as authored on the test.
type Variable struct {
	Name string `json:"name"`
	// Value is a literal or a builtin expression, e.g. `{{ alphanumeric(8) }}`.
	Value  string `json:"value"`
	Secure bool   `json:"secure"`
}

// PEMPair is a client certificate and its key.
type PEMPair struct {
	CertPEM    string `json:"cert_pem"`
	KeyPEM     string `json:"key_pem"`
	Passphrase string `json:"passphrase,omitempty"`
	// Origins are the exact origins (scheme://host:port) the certificate is
	// presented to; Playwright requires an exact match.
	Origins []string `json:"origins"`
}

// Step is one authored step. Params is decoded per Type by the runner's step
// registry; the shapes are the *Params types below.
type Step struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`

	Params json.RawMessage `json:"params"`

	// AllowFailure lets the run continue past this step failing; the step is
	// still reported as failed.
	AllowFailure bool `json:"allow_failure"`
	// TimeoutSec bounds this step. Zero means the runner's default.
	TimeoutSec int `json:"timeout_sec"`
	// NoScreenshot suppresses this step's screenshot (per-step privacy).
	NoScreenshot bool `json:"no_screenshot"`
}

// Options are the test-level "advanced options".
type Options struct {
	DisableCORS         bool              `json:"disable_cors"`
	DisableCSP          bool              `json:"disable_csp"`
	CaptureHTTPPayloads bool              `json:"capture_http_payloads"`
	Headers             map[string]string `json:"headers,omitempty"`
	// Cookies are in Set-Cookie syntax, one per entry.
	Cookies          []string    `json:"cookies,omitempty"`
	HTTPAuth         *HTTPAuth   `json:"http_auth,omitempty"`
	IgnoreCertErrors bool        `json:"ignore_cert_errors"`
	ClientCert       *ClientCert `json:"client_cert,omitempty"`
	ProxyURL         string      `json:"proxy_url,omitempty"`
	NoScreenshots    bool        `json:"no_screenshots"`
	// StartURLTimeoutSec bounds the initial navigation. Zero = 60.
	StartURLTimeoutSec int `json:"start_url_timeout_sec"`
	// Timezone is an IANA zone; default UTC. Language is a BCP-47 tag; default en.
	Timezone string `json:"timezone,omitempty"`
	Language string `json:"language,omitempty"`
	// BlockedRequests are Chrome match patterns (https://*.ads.example/*); a
	// matching request is aborted and recorded as blocked.
	BlockedRequests []string `json:"blocked_requests,omitempty"`
}

// HTTPAuth is HTTP authentication for the whole run.
type HTTPAuth struct {
	// Type is basic | digest | ntlm. Playwright negotiates whichever the server
	// challenges with, so the type is informational and validated only.
	Type     string `json:"type"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ClientCert references certificate material held by the backend. The runner
// receives the resolved PEMPair on RunSpec.ClientCert, never these refs.
type ClientCert struct {
	CertRef string   `json:"cert_ref"`
	KeyRef  string   `json:"key_ref"`
	Domains []string `json:"domains"`
}

// Element is how a step names the element it acts on. Several strategies are
// stored and tried in order — data-testid, CSS, XPath, visible text — because
// no single one survives every front-end change. The strategy that matched is
// reported on the step result so brittle locators are visible.
type Element struct {
	TestID string `json:"test_id,omitempty"`
	CSS    string `json:"css,omitempty"`
	XPath  string `json:"xpath,omitempty"`
	Text   string `json:"text,omitempty"`
	// Description is what the UI shows for this element ("Checkout button").
	Description string `json:"description,omitempty"`
}

// Empty reports whether no strategy is set.
func (e Element) Empty() bool {
	return e.TestID == "" && e.CSS == "" && e.XPath == "" && e.Text == ""
}

// Comparison operators for assertions on text.
const (
	OpIs          = "is"
	OpIsNot       = "is_not"
	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpStartsWith  = "starts_with"
	OpEndsWith    = "ends_with"
	OpMatches     = "matches"
	OpNotMatches  = "not_matches"
)

// ValidOp reports whether op is a known comparison.
func ValidOp(op string) bool {
	switch op {
	case OpIs, OpIsNot, OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpMatches, OpNotMatches:
		return true
	}
	return false
}

// Step types. The Phase column of the plan's table is which milestone ships it;
// the registry in the runner is the authority on what is implemented today.
const (
	StepGoToURL                = "go_to_url"
	StepRefresh                = "refresh"
	StepWait                   = "wait"
	StepScroll                 = "scroll"
	StepClick                  = "click"
	StepHover                  = "hover"
	StepTypeText               = "type_text"
	StepPressKey               = "press_key"
	StepSelectOption           = "select_option"
	StepAssertCurrentURL       = "assert_current_url"
	StepAssertPageContains     = "assert_page_contains"
	StepAssertPageLacks        = "assert_page_lacks"
	StepAssertElementPresent   = "assert_element_present"
	StepAssertElementContent   = "assert_element_content"
	StepAssertElementAttribute = "assert_element_attribute"
	StepAssertCheckboxState    = "assert_checkbox_state"
	StepExtractVariable        = "extract_variable"
	StepAssertFromJS           = "assert_from_js"
	StepExtractFromJS          = "extract_from_js"
	StepAssertFileDownload     = "assert_file_download"
	StepUploadFiles            = "upload_files"
	StepAssertRequests         = "assert_requests"
	StepRunAPITest             = "run_api_test"
)

// StepTypes lists every step type, in the order the UI offers them.
var StepTypes = []string{
	StepGoToURL, StepRefresh, StepWait, StepScroll,
	StepClick, StepHover, StepTypeText, StepPressKey, StepSelectOption,
	StepAssertCurrentURL, StepAssertPageContains, StepAssertPageLacks,
	StepAssertElementPresent, StepAssertElementContent, StepAssertElementAttribute, StepAssertCheckboxState,
	StepExtractVariable, StepAssertFromJS, StepExtractFromJS,
	StepAssertFileDownload, StepUploadFiles, StepAssertRequests, StepRunAPITest,
}

// ValidStepType reports whether t is a known step type.
func ValidStepType(t string) bool {
	for _, known := range StepTypes {
		if known == t {
			return true
		}
	}
	return false
}

// ── Step parameters, one struct per type ─────────────────────────────────────

type GoToURLParams struct {
	URL string `json:"url"`
}

type WaitParams struct {
	Seconds int `json:"seconds"`
}

// MaxWaitSeconds caps an explicit wait; a longer one is a scheduling problem,
// not a step.
const MaxWaitSeconds = 300

type ScrollParams struct {
	// Either an element to scroll into view, or a pixel offset.
	Element *Element `json:"element,omitempty"`
	X       int      `json:"x"`
	Y       int      `json:"y"`
}

type ClickParams struct {
	Element Element `json:"element"`
	// Button is left | right | middle; empty = left.
	Button string `json:"button,omitempty"`
	Double bool   `json:"double"`
}

type HoverParams struct {
	Element Element `json:"element"`
}

type TypeTextParams struct {
	Element Element `json:"element"`
	Text    string  `json:"text"`
	// Clear empties the field first. Default true.
	Clear *bool `json:"clear,omitempty"`
}

type PressKeyParams struct {
	Key       string   `json:"key"`
	Modifiers []string `json:"modifiers,omitempty"` // Shift, Control, Alt, Meta
}

type SelectOptionParams struct {
	Element Element `json:"element"`
	Value   string  `json:"value"`
}

type AssertCurrentURLParams struct {
	Op    string `json:"op"`
	Value string `json:"value"`
}

type AssertPageTextParams struct {
	Text string `json:"text"`
}

type AssertElementPresentParams struct {
	Element Element `json:"element"`
}

type AssertElementContentParams struct {
	Element Element `json:"element"`
	Op      string  `json:"op"`
	Value   string  `json:"value"`
}

type AssertElementAttributeParams struct {
	Element Element `json:"element"`
	Attr    string  `json:"attr"`
	Op      string  `json:"op"`
	Value   string  `json:"value"`
}

type AssertCheckboxStateParams struct {
	Element Element `json:"element"`
	Checked bool    `json:"checked"`
}

type ExtractVariableParams struct {
	Element Element `json:"element"`
	Name    string  `json:"name"`
	Secure  bool    `json:"secure"`
}

type AssertFromJSParams struct {
	// Code is a JavaScript expression or function body; a truthy result passes.
	Code string `json:"code"`
}

type ExtractFromJSParams struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Secure marks the extracted value for masking.
	Secure bool `json:"secure"`
}

type AssertFileDownloadParams struct {
	// Name matches the suggested file name (contains); empty = any download.
	Name string `json:"name,omitempty"`
	// Size in bytes; zero = not checked. MD5 hex; empty = not checked.
	Size int64  `json:"size,omitempty"`
	MD5  string `json:"md5,omitempty"`
}

// FileRef names a file the worker ships to the VM before the run and the
// runner feeds to a file input. Path is relative to the run folder.
type FileRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type UploadFilesParams struct {
	Element Element   `json:"element"`
	Files   []FileRef `json:"files"`
}

type AssertRequestsParams struct {
	// URLPattern is a Chrome match pattern or a substring.
	URLPattern string `json:"url_pattern"`
	Method     string `json:"method,omitempty"`
	// Status is the expected response status; zero = any.
	Status int `json:"status,omitempty"`
	// MinCount is how many matching requests must exist; zero = at least one.
	MinCount int `json:"min_count,omitempty"`
}

// APIAssertion is one check on a run_api_test response.
type APIAssertion struct {
	// Target is status_code | body | header | response_time_ms.
	Target string `json:"target"`
	// Header names the header when Target is header.
	Header string `json:"header,omitempty"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

type RunAPITestParams struct {
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Assertions []APIAssertion    `json:"assertions,omitempty"`
}

// ── Validation ───────────────────────────────────────────────────────────────

// Validate checks a RunSpec the way the runner will read it. The backend runs
// the same check when a test is saved, so a malformed step is refused in the
// editor rather than discovered by the first scheduled run.
func (s *RunSpec) Validate() error {
	var errs []error
	if strings.TrimSpace(s.RunID) == "" {
		errs = append(errs, errors.New("run_id is required"))
	}
	// The scheme is enforced here too, not only by the backend that built this
	// spec: the runner hands StartURL to a browser, so file:// or chrome://
	// would render this VM's own filesystem (see ValidHTTPURL).
	if err := ValidHTTPURL(s.StartURL); err != nil {
		errs = append(errs, fmt.Errorf("start_%w", err))
	}
	if !ValidBrowser(s.Browser) {
		errs = append(errs, fmt.Errorf("browser %q is not one of %s", s.Browser, strings.Join(Browsers, ", ")))
	}
	if !ValidDevice(s.Device) {
		errs = append(errs, fmt.Errorf("device %q is not one of %s", s.Device, strings.Join(Devices, ", ")))
	}
	if s.Retries < 0 || s.Retries > 5 {
		errs = append(errs, errors.New("retries must be between 0 and 5"))
	}
	if err := s.Options.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateSteps(s.Steps); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Validate checks the advanced options.
func (o *Options) Validate() error {
	var errs []error
	if o.HTTPAuth != nil {
		switch o.HTTPAuth.Type {
		case "basic", "digest", "ntlm":
		default:
			errs = append(errs, fmt.Errorf("http_auth.type %q must be basic, digest or ntlm", o.HTTPAuth.Type))
		}
	}
	if o.ClientCert != nil && (o.ClientCert.CertRef == "" || o.ClientCert.KeyRef == "") {
		errs = append(errs, errors.New("client_cert needs both cert_ref and key_ref"))
	}
	if o.StartURLTimeoutSec < 0 || o.StartURLTimeoutSec > 300 {
		errs = append(errs, errors.New("start_url_timeout_sec must be between 0 and 300"))
	}
	for _, c := range o.Cookies {
		if !strings.Contains(c, "=") {
			errs = append(errs, fmt.Errorf("cookie %q is not in Set-Cookie syntax (name=value; ...)", c))
		}
	}
	return errors.Join(errs...)
}

// ValidateSteps checks each step's type and parameters. Every problem is
// reported, prefixed with the step's position and name, so an editor can show
// them all at once.
func ValidateSteps(steps []Step) error {
	var errs []error
	seen := make(map[string]bool, len(steps))
	for i, st := range steps {
		prefix := fmt.Sprintf("step %d", i+1)
		if st.Name != "" {
			prefix += " (" + st.Name + ")"
		}
		if st.ID == "" {
			errs = append(errs, fmt.Errorf("%s: id is required", prefix))
		} else if seen[st.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id %q", prefix, st.ID))
		}
		seen[st.ID] = true
		if st.TimeoutSec < 0 || st.TimeoutSec > 300 {
			errs = append(errs, fmt.Errorf("%s: timeout_sec must be between 0 and 300", prefix))
		}
		if err := ValidateParams(st.Type, st.Params); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", prefix, err))
		}
	}
	return errors.Join(errs...)
}

// ValidateParams decodes and checks one step's parameters.
func ValidateParams(stepType string, raw json.RawMessage) error {
	if !ValidStepType(stepType) {
		return fmt.Errorf("unknown step type %q", stepType)
	}
	decode := func(v any) error {
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		return json.Unmarshal(raw, v)
	}
	needElement := func(e Element, what string) error {
		if e.Empty() {
			return fmt.Errorf("%s: element needs at least one locator (test_id, css, xpath or text)", what)
		}
		return nil
	}

	switch stepType {
	case StepGoToURL:
		var p GoToURLParams
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.URL) == "" {
			return errors.New("go_to_url: url is required")
		}
	case StepRefresh:
	case StepWait:
		var p WaitParams
		if err := decode(&p); err != nil {
			return err
		}
		if p.Seconds < 1 || p.Seconds > MaxWaitSeconds {
			return fmt.Errorf("wait: seconds must be between 1 and %d", MaxWaitSeconds)
		}
	case StepScroll:
		var p ScrollParams
		if err := decode(&p); err != nil {
			return err
		}
		if p.Element != nil && p.Element.Empty() {
			return errors.New("scroll: element needs at least one locator, or omit it and give x/y")
		}
	case StepClick:
		var p ClickParams
		if err := decode(&p); err != nil {
			return err
		}
		if err := needElement(p.Element, "click"); err != nil {
			return err
		}
		switch p.Button {
		case "", "left", "right", "middle":
		default:
			return fmt.Errorf("click: button %q must be left, right or middle", p.Button)
		}
	case StepHover:
		var p HoverParams
		if err := decode(&p); err != nil {
			return err
		}
		return needElement(p.Element, "hover")
	case StepTypeText:
		var p TypeTextParams
		if err := decode(&p); err != nil {
			return err
		}
		return needElement(p.Element, "type_text")
	case StepPressKey:
		var p PressKeyParams
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.Key) == "" {
			return errors.New("press_key: key is required")
		}
		for _, m := range p.Modifiers {
			switch m {
			case "Shift", "Control", "Alt", "Meta":
			default:
				return fmt.Errorf("press_key: modifier %q must be Shift, Control, Alt or Meta", m)
			}
		}
	case StepSelectOption:
		var p SelectOptionParams
		if err := decode(&p); err != nil {
			return err
		}
		return needElement(p.Element, "select_option")
	case StepAssertCurrentURL:
		var p AssertCurrentURLParams
		if err := decode(&p); err != nil {
			return err
		}
		if !ValidOp(p.Op) {
			return fmt.Errorf("assert_current_url: op %q is not a known comparison", p.Op)
		}
	case StepAssertPageContains, StepAssertPageLacks:
		var p AssertPageTextParams
		if err := decode(&p); err != nil {
			return err
		}
		if p.Text == "" {
			return fmt.Errorf("%s: text is required", stepType)
		}
	case StepAssertElementPresent:
		var p AssertElementPresentParams
		if err := decode(&p); err != nil {
			return err
		}
		return needElement(p.Element, stepType)
	case StepAssertElementContent:
		var p AssertElementContentParams
		if err := decode(&p); err != nil {
			return err
		}
		if err := needElement(p.Element, stepType); err != nil {
			return err
		}
		if !ValidOp(p.Op) {
			return fmt.Errorf("%s: op %q is not a known comparison", stepType, p.Op)
		}
	case StepAssertElementAttribute:
		var p AssertElementAttributeParams
		if err := decode(&p); err != nil {
			return err
		}
		if err := needElement(p.Element, stepType); err != nil {
			return err
		}
		if p.Attr == "" {
			return errors.New("assert_element_attribute: attr is required")
		}
		if !ValidOp(p.Op) {
			return fmt.Errorf("%s: op %q is not a known comparison", stepType, p.Op)
		}
	case StepAssertCheckboxState:
		var p AssertCheckboxStateParams
		if err := decode(&p); err != nil {
			return err
		}
		return needElement(p.Element, stepType)
	case StepExtractVariable:
		var p ExtractVariableParams
		if err := decode(&p); err != nil {
			return err
		}
		if err := needElement(p.Element, stepType); err != nil {
			return err
		}
		return validVarName(p.Name, stepType)
	case StepAssertFromJS:
		var p AssertFromJSParams
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.Code) == "" {
			return errors.New("assert_from_js: code is required")
		}
	case StepExtractFromJS:
		var p ExtractFromJSParams
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.Code) == "" {
			return errors.New("extract_from_js: code is required")
		}
		return validVarName(p.Name, stepType)
	case StepAssertFileDownload:
		var p AssertFileDownloadParams
		if err := decode(&p); err != nil {
			return err
		}
		if p.MD5 != "" && len(p.MD5) != 32 {
			return errors.New("assert_file_download: md5 must be 32 hex characters")
		}
	case StepUploadFiles:
		var p UploadFilesParams
		if err := decode(&p); err != nil {
			return err
		}
		if err := needElement(p.Element, stepType); err != nil {
			return err
		}
		if len(p.Files) == 0 {
			return errors.New("upload_files: at least one file is required")
		}
	case StepAssertRequests:
		var p AssertRequestsParams
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.URLPattern) == "" {
			return errors.New("assert_requests: url_pattern is required")
		}
	case StepRunAPITest:
		var p RunAPITestParams
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.URL) == "" {
			return errors.New("run_api_test: url is required")
		}
		switch strings.ToUpper(p.Method) {
		case "", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			return fmt.Errorf("run_api_test: method %q is not an HTTP method", p.Method)
		}
		for i, a := range p.Assertions {
			switch a.Target {
			case "status_code", "body", "header", "response_time_ms":
			default:
				return fmt.Errorf("run_api_test: assertion %d target %q must be status_code, body, header or response_time_ms", i+1, a.Target)
			}
			if !ValidOp(a.Op) && a.Op != "less_than" && a.Op != "greater_than" {
				return fmt.Errorf("run_api_test: assertion %d op %q is not a known comparison", i+1, a.Op)
			}
		}
	}
	return nil
}

// validVarName mirrors the Datadog convention: UPPER_SNAKE_CASE, letters,
// digits and underscore, at least three characters.
func validVarName(name, stepType string) error {
	if len(name) < 3 {
		return fmt.Errorf("%s: variable name must be at least 3 characters", stepType)
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return fmt.Errorf("%s: variable name %q must start with a letter", stepType, name)
			}
		default:
			return fmt.Errorf("%s: variable name %q must be UPPER_SNAKE_CASE", stepType, name)
		}
	}
	return nil
}

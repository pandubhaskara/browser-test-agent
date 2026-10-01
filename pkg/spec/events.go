package spec

import "time"

// Event names the runner emits, one JSON object per line on stdout. The worker
// ingests them live; unknown names are logged and ignored so an older worker
// keeps working against a newer runner.
const (
	EvRunStarted      = "run_started"
	EvAttemptStarted  = "attempt_started"
	EvStepStarted     = "step_started"
	EvStepFinished    = "step_finished"
	EvAttemptFinished = "attempt_finished"
	EvRunFinished     = "run_finished"
	EvLog             = "log"
)

// Run and step statuses, shared by events and the backend's stored rows.
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Event is one line of the NDJSON stream. It is a single flat struct rather
// than one type per event so a decoder can read the whole stream with one
// type and switch on Ev; fields that do not apply to an event are omitted.
type Event struct {
	Ev   string    `json:"ev"`
	Time time.Time `json:"ts"`

	RunID   string `json:"run_id,omitempty"`
	Attempt int    `json:"attempt,omitempty"`

	// run_started
	Browser        string `json:"browser,omitempty"`
	BrowserVersion string `json:"browser_version,omitempty"`
	Device         string `json:"device,omitempty"`
	RunnerVersion  string `json:"runner_version,omitempty"`

	// step_started / step_finished
	StepID   string `json:"step_id,omitempty"`
	StepType string `json:"step_type,omitempty"`
	StepName string `json:"step_name,omitempty"`
	// Status is passed | failed | error | skipped for steps, attempts and the run.
	Status     string `json:"status,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	// Screenshot is the file name inside the run folder, empty when none was
	// taken (privacy option, or the step failed before the page existed).
	Screenshot string `json:"screenshot,omitempty"`
	// LocatorUsed is which Element strategy matched: test_id | css | xpath | text.
	LocatorUsed string `json:"locator_used,omitempty"`
	// Extracted is the variable a step bound, already masked when secure.
	Extracted *Extracted `json:"extracted,omitempty"`
	// AllowFailure is echoed on a failed step so the ingester can record that
	// the run continued past it.
	AllowFailure bool `json:"allow_failure,omitempty"`

	// run_finished
	FailedStepID string `json:"failed_step_id,omitempty"`
	// Resources is the resources file name inside the run folder.
	Resources string `json:"resources,omitempty"`
	// Result is the NDJSON copy of the whole stream inside the run folder.
	Result string `json:"result,omitempty"`

	// log
	Message string `json:"message,omitempty"`
}

// Extracted is a variable a step bound during the run.
type Extracted struct {
	Name string `json:"name"`
	// Value is masked ("********") when Secure.
	Value  string `json:"value"`
	Secure bool   `json:"secure"`
}

// Resource is one network request observed during a run, written to
// resources.json as a JSON array.
type Resource struct {
	URL        string `json:"url"`
	Method     string `json:"method"`
	Type       string `json:"type"` // document | script | xhr | fetch | image | ...
	Status     int    `json:"status,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	// Blocked is true when Options.BlockedRequests aborted it.
	Blocked bool `json:"blocked,omitempty"`
	// Failed is set when the request never got a response.
	Failed string `json:"failed,omitempty"`
	// Payload is the file under payloads/ holding headers and body, only with
	// Options.CaptureHTTPPayloads and only for fetch/XHR.
	Payload string `json:"payload,omitempty"`
	// StepID is the step during which the request was observed.
	StepID string `json:"step_id,omitempty"`
}

// Artifact file names inside runs/<run_id>/, so the worker's collector and the
// runner agree without either guessing.
const (
	ResultFile    = "result.ndjson"
	ResourcesFile = "resources.json"
	PayloadsDir   = "payloads"
	UploadsDir    = "uploads"
)

// ScreenshotName is the file for step n (1-based): step-001.png.
func ScreenshotName(n int) string {
	return "step-" + pad3(n) + ".png"
}

func pad3(n int) string {
	s := itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

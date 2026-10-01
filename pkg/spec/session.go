package spec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// The `session` command holds a real browser open on the location VM and
// streams it to the editor: frames out, input in, one NDJSON object per line
// over the same SSH exec. This file is the vocabulary both ends share, so the
// backend never hand-rolls the wire format.
//
// Unlike `preview`, which answers once and exits, a session is a conversation:
// the first line of stdin is the RunSpec, every line after it is an input
// event, and stdout carries events until the session closes.

// Input event types (editor → agent).
const (
	SessionInNavigate = "navigate"
	SessionInBack     = "back"
	SessionInForward  = "forward"
	SessionInReload   = "reload"
	SessionInMouse    = "mouse"
	SessionInKey      = "key"
	SessionInText     = "text"
	SessionInPick     = "pick"
	SessionInViewport = "viewport"
	SessionInClose    = "close"
	SessionInPing     = "ping"
)

// Mouse actions carried by SessionInMouse.
const (
	MouseMove     = "move"
	MouseDown     = "down"
	MouseUp       = "up"
	MouseClick    = "click"
	MouseDblclick = "dblclick"
	MouseWheel    = "wheel"
)

// Output event types (agent → editor).
const (
	SessionOutReady  = "ready"
	SessionOutFrame  = "frame"
	SessionOutNav    = "nav"
	SessionOutPicked = "picked"
	SessionOutError  = "error"
	SessionOutClosed = "closed"
)

// SessionMaxFrameB64 drops a frame rather than risk the worker's 4 MiB line
// limit. A capped-width JPEG is tens of kilobytes, so this only trips on a
// pathological screencast.
const SessionMaxFrameB64 = 2 << 20

// SessionStart is the first line of a session's stdin.
//
// PickerScript travels with the request rather than being embedded in the
// agent so the editor and the selector rules stay one source of truth on the
// backend: fixing a selector then needs no runner rebuild and no location
// bootstrap.
//
// MaxWidth and MaxHeight cap the streamed image, not the page: the browser
// keeps the device's viewport so the layout is the one the test will see, and
// the editor scales its clicks back using the viewport reported in `ready`.
type SessionStart struct {
	Spec         *RunSpec `json:"spec"`
	PickerScript string   `json:"picker_script,omitempty"`
	MaxWidth     int      `json:"max_width,omitempty"`
	MaxHeight    int      `json:"max_height,omitempty"`
	Quality      int      `json:"quality,omitempty"`
	IdleSeconds  int      `json:"idle_seconds,omitempty"`
	MaxSeconds   int      `json:"max_seconds,omitempty"`
}

// Session defaults, applied by ApplyDefaults.
const (
	SessionDefaultMaxWidth  = 1000
	SessionDefaultQuality   = 55
	SessionDefaultIdleSecs  = 120
	SessionDefaultMaxSecs   = 900
	sessionMinQuality       = 10
	sessionMaxQuality       = 95
	sessionMaxStreamedWidth = 2000
)

// ApplyDefaults fills the unset knobs and clamps the rest, so a caller can
// send only what it cares about and a bad value cannot make the stream
// unusable.
func (s *SessionStart) ApplyDefaults() {
	if s.MaxWidth <= 0 {
		s.MaxWidth = SessionDefaultMaxWidth
	}
	if s.MaxWidth > sessionMaxStreamedWidth {
		s.MaxWidth = sessionMaxStreamedWidth
	}
	if s.MaxHeight <= 0 {
		// A tall page is scrolled, not shrunk: bound the height generously and
		// let maxWidth do the real work.
		s.MaxHeight = s.MaxWidth * 2
	}
	if s.Quality <= 0 {
		s.Quality = SessionDefaultQuality
	}
	if s.Quality < sessionMinQuality {
		s.Quality = sessionMinQuality
	}
	if s.Quality > sessionMaxQuality {
		s.Quality = sessionMaxQuality
	}
	if s.IdleSeconds <= 0 {
		s.IdleSeconds = SessionDefaultIdleSecs
	}
	if s.MaxSeconds <= 0 {
		s.MaxSeconds = SessionDefaultMaxSecs
	}
}

// Validate checks the start payload carries a usable spec.
func (s *SessionStart) Validate() error {
	if s.Spec == nil {
		return fmt.Errorf("session start has no spec")
	}
	return s.Spec.Validate()
}

// SessionInput is one instruction for the live browser.
type SessionInput struct {
	Type string `json:"type"`

	// Navigate.
	URL string `json:"url,omitempty"`

	// Mouse: one of the Mouse* actions. X and Y are CSS pixels in the page's
	// viewport, which is what the editor scales its own coordinates to.
	Action string  `json:"action,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button string  `json:"button,omitempty"`
	DeltaX float64 `json:"delta_x,omitempty"`
	DeltaY float64 `json:"delta_y,omitempty"`

	// Key is a Playwright key name (Enter, Tab, ArrowDown, a); Text is typed
	// literally, which is how a paste or an IME composition arrives.
	Key  string `json:"key,omitempty"`
	Text string `json:"text,omitempty"`

	// Viewport resizes the page and the screencast to the editor's panel.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// Validate rejects an input the session cannot act on, so a malformed line is
// answered rather than silently ignored.
func (in SessionInput) Validate() error {
	switch in.Type {
	case SessionInBack, SessionInForward, SessionInReload, SessionInClose, SessionInPing:
		return nil
	case SessionInNavigate:
		return ValidHTTPURL(in.URL)
	case SessionInPick:
		return nil
	case SessionInMouse:
		switch in.Action {
		case MouseMove, MouseDown, MouseUp, MouseClick, MouseDblclick, MouseWheel:
			return nil
		}
		return fmt.Errorf("unknown mouse action %q", in.Action)
	case SessionInKey:
		if strings.TrimSpace(in.Key) == "" {
			return fmt.Errorf("key is required")
		}
		return nil
	case SessionInText:
		if in.Text == "" {
			return fmt.Errorf("text is required")
		}
		return nil
	case SessionInViewport:
		if in.Width <= 0 || in.Height <= 0 {
			return fmt.Errorf("viewport needs a positive width and height")
		}
		return nil
	}
	return fmt.Errorf("unknown input type %q", in.Type)
}

// SessionEvent is one thing the live browser reports.
type SessionEvent struct {
	Type string `json:"type"`

	// Ready and nav: where the page is now.
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	CanGoBack bool   `json:"can_go_back,omitempty"`

	// Frame: a JPEG, base64 because stdout is line-based.
	//
	// Width and height here are the page's viewport in CSS pixels, from the
	// screencast metadata — not the JPEG's pixel size, which MaxWidth caps
	// independently. The editor scales its clicks using the viewport reported
	// in `ready` and the box the image actually occupies, so these are
	// informational: they say when the page's own viewport changed.
	B64    string `json:"b64,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`

	// Picked: the locator strategies for the element the user clicked.
	Element *Element `json:"element,omitempty"`

	// Error and closed.
	Message string `json:"message,omitempty"`
}

// EncodeSessionEvent writes one event as a line.
func EncodeSessionEvent(w io.Writer, ev SessionEvent) error {
	return json.NewEncoder(w).Encode(ev)
}

// DecodeSessionEvent reads one.
func DecodeSessionEvent(line []byte) (SessionEvent, error) {
	var ev SessionEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, fmt.Errorf("session event is not readable: %s", excerptBytes(line))
	}
	if ev.Type == "" {
		return ev, fmt.Errorf("session event has no type: %s", excerptBytes(line))
	}
	return ev, nil
}

// EncodeSessionInput writes one input as a line.
func EncodeSessionInput(w io.Writer, in SessionInput) error {
	return json.NewEncoder(w).Encode(in)
}

// DecodeSessionInput reads one.
func DecodeSessionInput(line []byte) (SessionInput, error) {
	var in SessionInput
	if err := json.Unmarshal(line, &in); err != nil {
		return in, fmt.Errorf("session input is not readable: %s", excerptBytes(line))
	}
	return in, nil
}

// ValidHTTPURL is the rule for any URL a browser test opens, enforced on the
// VM as well as by the backend that sent it. The scheme is a boundary, not a
// formality: file:// or chrome:// would render this VM's own filesystem and
// hand it back to whoever is watching the session.
//
// A URL still holding a {{ variable }} is exempt, because it is resolved
// against the run's scope later; the resolved form is checked at that point.
func ValidHTTPURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("url is required")
	}
	if strings.Contains(s, "{{") {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("url is not valid: %s", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		if scheme == "" {
			return fmt.Errorf("url must be an http(s) URL")
		}
		return fmt.Errorf("url must be an http(s) URL, not %s", scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url must be an http(s) URL")
	}
	return nil
}

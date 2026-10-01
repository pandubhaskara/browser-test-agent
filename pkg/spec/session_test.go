package spec

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionEventRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := []SessionEvent{
		{Type: SessionOutReady, URL: "https://shop.example/", Title: "Shop"},
		{Type: SessionOutFrame, B64: "aGVsbG8=", Width: 900, Height: 700},
		{Type: SessionOutNav, URL: "https://shop.example/cart", Title: "Cart", CanGoBack: true},
		{Type: SessionOutPicked, Element: &Element{TestID: "buy", CSS: "#buy", XPath: "//*[@id=\"buy\"]", Text: "Buy", Description: `button "Buy"`}},
		{Type: SessionOutError, Message: "could not open https://shop.example/: timeout"},
		{Type: SessionOutClosed, Message: "idle"},
	}
	for _, ev := range want {
		if err := EncodeSessionEvent(&buf, ev); err != nil {
			t.Fatal(err)
		}
	}

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i, line := range lines {
		got, err := DecodeSessionEvent(line)
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if got.Type != want[i].Type || got.URL != want[i].URL || got.B64 != want[i].B64 || got.Message != want[i].Message {
			t.Errorf("line %d: got %+v want %+v", i, got, want[i])
		}
	}

	// The picked element survives whole, since it becomes the step's locators.
	picked, err := DecodeSessionEvent(lines[3])
	if err != nil {
		t.Fatal(err)
	}
	if picked.Element == nil || picked.Element.CSS != "#buy" || picked.Element.Text != "Buy" {
		t.Fatalf("element lost: %+v", picked.Element)
	}
}

func TestDecodeSessionEvent_Rejects(t *testing.T) {
	if _, err := DecodeSessionEvent([]byte("not json")); err == nil {
		t.Error("expected an error for garbage")
	}
	if _, err := DecodeSessionEvent([]byte(`{"url":"x"}`)); err == nil {
		t.Error("expected an error for a typeless event")
	}
}

func TestSessionInputRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := SessionInput{Type: SessionInMouse, Action: MouseClick, X: 12.5, Y: 40, Button: "left"}
	if err := EncodeSessionInput(&buf, in); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSessionInput(bytes.TrimSpace(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("got %+v want %+v", got, in)
	}
}

func TestSessionInputValidate(t *testing.T) {
	ok := []SessionInput{
		{Type: SessionInBack},
		{Type: SessionInForward},
		{Type: SessionInReload},
		{Type: SessionInClose},
		{Type: SessionInPing},
		{Type: SessionInPick, X: 1, Y: 2},
		{Type: SessionInNavigate, URL: "https://shop.example/"},
		{Type: SessionInNavigate, URL: "{{ BASE_URL }}/login"},
		{Type: SessionInMouse, Action: MouseMove, X: 1, Y: 2},
		{Type: SessionInMouse, Action: MouseWheel, DeltaY: 120},
		{Type: SessionInKey, Key: "Enter"},
		{Type: SessionInText, Text: "hello"},
		{Type: SessionInViewport, Width: 900, Height: 700},
	}
	for _, in := range ok {
		if err := in.Validate(); err != nil {
			t.Errorf("%+v should be accepted: %v", in, err)
		}
	}

	bad := []struct {
		in   SessionInput
		want string
	}{
		{SessionInput{Type: "teleport"}, "unknown input type"},
		{SessionInput{Type: SessionInMouse, Action: "lick"}, "unknown mouse action"},
		{SessionInput{Type: SessionInKey}, "key is required"},
		{SessionInput{Type: SessionInText}, "text is required"},
		{SessionInput{Type: SessionInViewport, Width: 0, Height: 700}, "positive width"},
		{SessionInput{Type: SessionInNavigate, URL: "file:///etc/passwd"}, "not file"},
		{SessionInput{Type: SessionInNavigate, URL: ""}, "required"},
	}
	for _, tc := range bad {
		err := tc.in.Validate()
		if err == nil {
			t.Errorf("%+v should be refused", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: message %q should mention %q", tc.in, err.Error(), tc.want)
		}
	}
}

func TestValidHTTPURL(t *testing.T) {
	for _, raw := range []string{"https://a.test/x", "http://10.0.0.1:8080/", "HTTPS://A.test", "{{ VAR }}/x"} {
		if err := ValidHTTPURL(raw); err != nil {
			t.Errorf("%q should be accepted: %v", raw, err)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{"file:///etc/passwd", "not file"},
		{"chrome://version", "not chrome"},
		{"data:text/html,x", "not data"},
		{"a.test/x", "must be an http(s) URL"},
		{"", "required"},
	} {
		err := ValidHTTPURL(tc.raw)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.raw, err, tc.want)
		}
	}
}

func TestSessionStart_ApplyDefaultsAndClamps(t *testing.T) {
	var s SessionStart
	s.ApplyDefaults()
	if s.MaxWidth != SessionDefaultMaxWidth || s.Quality != SessionDefaultQuality {
		t.Fatalf("defaults not applied: %+v", s)
	}
	if s.MaxHeight != s.MaxWidth*2 {
		t.Fatalf("height should follow width: %+v", s)
	}
	if s.IdleSeconds != SessionDefaultIdleSecs || s.MaxSeconds != SessionDefaultMaxSecs {
		t.Fatalf("timeouts not applied: %+v", s)
	}

	// A bad value must not be able to make the stream unusable.
	wide := SessionStart{MaxWidth: 99999, Quality: 500}
	wide.ApplyDefaults()
	if wide.MaxWidth != sessionMaxStreamedWidth || wide.Quality != sessionMaxQuality {
		t.Fatalf("not clamped upward: %+v", wide)
	}
	// A quality that is set but too low is raised to the floor; one that is
	// unset or nonsensical falls back to the default instead.
	low := SessionStart{Quality: 5}
	low.ApplyDefaults()
	if low.Quality != sessionMinQuality {
		t.Fatalf("not clamped to the floor: %+v", low)
	}
	negative := SessionStart{Quality: -5}
	negative.ApplyDefaults()
	if negative.Quality != SessionDefaultQuality {
		t.Fatalf("a nonsensical quality should take the default: %+v", negative)
	}
}

func TestSessionStart_Validate(t *testing.T) {
	var none SessionStart
	if err := none.Validate(); err == nil || !strings.Contains(err.Error(), "no spec") {
		t.Fatalf("got %v", err)
	}
	// The spec's own rules still apply, including the URL scheme.
	bad := SessionStart{Spec: &RunSpec{RunID: "r", StartURL: "file:///etc/passwd", Browser: BrowserChrome, Device: DeviceLaptopLarge}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("got %v", err)
	}
	ok := SessionStart{Spec: &RunSpec{RunID: "r", StartURL: "https://a.test/", Browser: BrowserChrome, Device: DeviceLaptopLarge}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

package session

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/pkg/spec"
)

// The browser-driving half of a session needs a real Chromium and a real VM,
// so it is verified by hand against a location. What is covered here is
// everything that can go wrong without one: the wire discipline on stdout, the
// input mapping, and the refusal of inputs the session cannot act on.

func TestEmitter_WritesOneLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	out := &emitter{w: &buf}
	out.event(spec.SessionEvent{Type: spec.SessionOutReady, URL: "https://a.test/"})
	out.error("boom")
	out.closed("idle")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	types := []string{spec.SessionOutReady, spec.SessionOutError, spec.SessionOutClosed}
	for i, line := range lines {
		ev, err := spec.DecodeSessionEvent(line)
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if ev.Type != types[i] {
			t.Errorf("line %d: got %s want %s", i, ev.Type, types[i])
		}
	}
}

// Frames arrive from the screencast callback while events come from the input
// loop, so a half-written line would break the reader on the backend.
func TestEmitter_SerialisesConcurrentWriters(t *testing.T) {
	var buf bytes.Buffer
	out := &emitter{w: &buf}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				out.event(spec.SessionEvent{Type: spec.SessionOutFrame, B64: strings.Repeat("a", 64)})
			}
		}()
	}
	wg.Wait()

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 200 {
		t.Fatalf("got %d lines, want 200", len(lines))
	}
	for i, line := range lines {
		if _, err := spec.DecodeSessionEvent(line); err != nil {
			t.Fatalf("line %d is not a whole event: %v", i, err)
		}
	}
}

func TestMouseButton(t *testing.T) {
	cases := map[string]*playwright.MouseButton{
		"":       playwright.MouseButtonLeft,
		"left":   playwright.MouseButtonLeft,
		"middle": playwright.MouseButtonMiddle,
		"right":  playwright.MouseButtonRight,
		"nonces": playwright.MouseButtonLeft,
	}
	for name, want := range cases {
		if got := mouseButton(name); got != want {
			t.Errorf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestApply_RejectsAnUnknownType(t *testing.T) {
	// apply is reached only after Validate, but it must not silently succeed on
	// something it cannot do — a nil page would panic if it tried.
	err := apply(nil, spec.SessionInput{Type: "teleport"}, &emitter{w: &bytes.Buffer{}}, func(s string) string { return s })
	if err == nil || !strings.Contains(err.Error(), "unknown input type") {
		t.Fatalf("got %v", err)
	}
}

func TestApply_PingIsANoOp(t *testing.T) {
	// Ping exists to reset the idle timer, so it must not touch the page.
	if err := apply(nil, spec.SessionInput{Type: spec.SessionInPing}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

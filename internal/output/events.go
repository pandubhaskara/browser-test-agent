// Package output writes the runner's NDJSON event stream: one JSON object per
// line to stdout for the worker to ingest live, and the same lines to
// result.ndjson in the run folder for the collector to keep.
//
// Every line passes through the masker on its way out. This is the choke point
// for secrets: a secure variable that reached an error message, a screenshot
// label or an extracted value is scrubbed here whether or not the code that
// produced it remembered to.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"browser-test-agent/pkg/spec"
	"browser-test-agent/pkg/vars"
)

// Emitter writes events. Safe for concurrent use: network hooks and the step
// loop both emit.
type Emitter struct {
	mu     sync.Mutex
	stdout io.Writer
	file   io.WriteCloser
	mask   *vars.Masker
	runID  string
	now    func() time.Time
}

// New creates an emitter writing to stdout and, when resultPath is not empty,
// tee-ing to that file. A file that cannot be opened is reported and skipped:
// the live stream is the one the worker needs; the file is a convenience copy.
func New(stdout io.Writer, resultPath, runID string, mask *vars.Masker) *Emitter {
	e := &Emitter{stdout: stdout, mask: mask, runID: runID, now: time.Now}
	if resultPath != "" {
		f, err := os.OpenFile(resultPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot write %s: %v (events go to stdout only)\n", resultPath, err)
		} else {
			e.file = f
		}
	}
	return e
}

// Emit writes one event. The run ID and timestamp are stamped here so no
// caller has to remember them.
func (e *Emitter) Emit(ev spec.Event) {
	ev.RunID = e.runID
	ev.Time = e.now().UTC()
	ev.Error = e.scrub(ev.Error)
	ev.Message = e.scrub(ev.Message)
	if ev.Extracted != nil && ev.Extracted.Secure {
		ev.Extracted.Value = vars.Mask
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		// Event holds only strings, ints and a small struct; unreachable, but
		// the stream must never lose its framing.
		raw = []byte(fmt.Sprintf(`{"ev":"log","run_id":%q,"message":"event marshal failed: %s"}`, e.runID, e.scrub(err.Error())))
	}
	// Masked again as a whole line: a secret spliced across two fields is
	// still one secret in the JSON text.
	line := append([]byte(e.scrub(string(raw))), '\n')

	e.mu.Lock()
	defer e.mu.Unlock()
	e.stdout.Write(line)
	if e.file != nil {
		e.file.Write(line)
	}
}

// Log emits a free-text log event.
func (e *Emitter) Log(format string, args ...any) {
	e.Emit(spec.Event{Ev: spec.EvLog, Message: fmt.Sprintf(format, args...)})
}

// Scrub masks arbitrary text, for callers writing files (resources.json,
// payloads) that bypass Emit.
func (e *Emitter) Scrub(s string) string { return e.scrub(s) }

func (e *Emitter) scrub(s string) string {
	if e.mask == nil || s == "" {
		return s
	}
	return e.mask.Mask(s)
}

// Close flushes and closes the result file.
func (e *Emitter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.file != nil {
		err := e.file.Close()
		e.file = nil
		return err
	}
	return nil
}

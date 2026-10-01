package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"browser-test-agent/pkg/spec"
	"browser-test-agent/pkg/vars"
)

func TestEmitWritesOneMaskedLineToBothSinks(t *testing.T) {
	mask := vars.NewMasker()
	mask.Add("example-value")
	var stdout bytes.Buffer
	result := filepath.Join(t.TempDir(), spec.ResultFile)

	e := New(&stdout, result, "run-1", mask)
	e.Emit(spec.Event{Ev: spec.EvStepFinished, StepID: "s1", Status: spec.StatusFailed, Error: "typed example-value into #pw"})
	e.Emit(spec.Event{Ev: spec.EvStepFinished, StepID: "s2", Extracted: &spec.Extracted{Name: "TOKEN", Value: "example-value", Secure: true}})
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines on stdout, want 2:\n%s", len(lines), stdout.String())
	}
	if strings.Contains(stdout.String(), "example-value") {
		t.Errorf("secret leaked to stdout: %s", stdout.String())
	}

	var first spec.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.RunID != "run-1" || first.Time.IsZero() {
		t.Errorf("run id / timestamp not stamped: %+v", first)
	}
	if first.Error != "typed "+vars.Mask+" into #pw" {
		t.Errorf("error not masked: %q", first.Error)
	}

	var second spec.Event
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second.Extracted == nil || second.Extracted.Value != vars.Mask {
		t.Errorf("secure extracted value not masked: %+v", second.Extracted)
	}

	fileBytes, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(fileBytes) != stdout.String() {
		t.Error("result.ndjson differs from stdout; the collector would keep something other than what the worker saw")
	}
}

func TestEmitWithoutResultFile(t *testing.T) {
	var stdout bytes.Buffer
	e := New(&stdout, "", "run-2", nil)
	e.Log("hello %d", 1)
	e.Close()
	if !strings.Contains(stdout.String(), `"message":"hello 1"`) {
		t.Errorf("got %s", stdout.String())
	}
}

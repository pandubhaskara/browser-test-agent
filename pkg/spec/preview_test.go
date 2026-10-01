package spec

import (
	"bytes"
	"strings"
	"testing"
)

func TestPreviewRoundTrip(t *testing.T) {
	html := strings.Repeat(`<div class="x">&amp; "quoted" <b>bold</b></div>`+"\n", 60_000) // ~3 MB, dense in escapes
	var out bytes.Buffer
	if err := EncodePreview(&out, PreviewEvent{URL: "https://a.test/final", Title: "T", Status: 200}, html); err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(out.Bytes(), []byte("\n")) {
		if len(line) > 1<<20 {
			t.Fatalf("line of %d bytes exceeds the 1 MiB budget", len(line))
		}
	}
	meta, got, err := DecodePreviewStream(&out)
	if err != nil {
		t.Fatal(err)
	}
	if meta.URL != "https://a.test/final" || meta.Title != "T" || meta.Status != 200 {
		t.Fatalf("meta %+v", meta)
	}
	if got != html {
		t.Fatalf("html mismatch: %d vs %d bytes", len(got), len(html))
	}
}

func TestPreviewEmptyPage(t *testing.T) {
	var out bytes.Buffer
	if err := EncodePreview(&out, PreviewEvent{URL: "https://a.test/"}, ""); err != nil {
		t.Fatal(err)
	}
	_, got, err := DecodePreviewStream(&out)
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPreviewErrorLine(t *testing.T) {
	var out bytes.Buffer
	_ = EncodePreviewError(&out, "could not open https://a.test/: net::ERR_NAME_NOT_RESOLVED")
	_, _, err := DecodePreviewStream(&out)
	if err == nil || !strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
		t.Fatalf("expected the agent's message, got %v", err)
	}
}

func TestPreviewTruncated(t *testing.T) {
	var out bytes.Buffer
	_ = EncodePreview(&out, PreviewEvent{URL: "u"}, "<p>x</p>")
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	_, _, err := DecodePreview(lines[:len(lines)-1])
	if err == nil || !strings.Contains(err.Error(), "ended before") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func TestPreviewTooLarge(t *testing.T) {
	var out bytes.Buffer
	_ = EncodePreview(&out, PreviewEvent{URL: "u"}, strings.Repeat("a", PreviewMaxHTMLBytes+1))
	_, _, err := DecodePreviewStream(&out)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestPreviewGarbage(t *testing.T) {
	_, _, err := DecodePreview([][]byte{[]byte("not json")})
	if err == nil {
		t.Fatal("expected error")
	}
}

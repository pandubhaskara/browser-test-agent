package spec

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// The `preview` command renders a page for the editor and answers on stdout
// with NDJSON, like `run`: one meta line, the page HTML in base64 chunks, and
// a done line — or a single error line. Chunking keeps every line well under
// the worker's 4 MiB scanner limit; base64 because JSON escaping of `<>&` would
// otherwise inflate an HTML chunk several times over.

// Preview event types.
const (
	PreviewMeta  = "meta"
	PreviewHTML  = "html"
	PreviewDone  = "done"
	PreviewError = "error"
)

// PreviewChunkBytes is how much raw HTML one html line carries.
const PreviewChunkBytes = 512 << 10

// PreviewMaxHTMLBytes bounds a rendered page; larger pages are refused
// rather than streamed.
const PreviewMaxHTMLBytes = 8 << 20

// PreviewEvent is one stdout line of the preview command.
type PreviewEvent struct {
	Type string `json:"type"`
	// Meta: where the page ended after redirects, its title, and the main
	// response's HTTP status (0 when unknown).
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`
	// HTML chunk, base64.
	B64 string `json:"b64,omitempty"`
	// Error message, for the user.
	Message string `json:"message,omitempty"`
}

// EncodePreview writes meta, the html chunks and done.
func EncodePreview(w io.Writer, meta PreviewEvent, html string) error {
	enc := json.NewEncoder(w)
	meta.Type = PreviewMeta
	if err := enc.Encode(meta); err != nil {
		return err
	}
	for start := 0; start < len(html); start += PreviewChunkBytes {
		end := start + PreviewChunkBytes
		if end > len(html) {
			end = len(html)
		}
		if err := enc.Encode(PreviewEvent{Type: PreviewHTML, B64: base64.StdEncoding.EncodeToString([]byte(html[start:end]))}); err != nil {
			return err
		}
	}
	return enc.Encode(PreviewEvent{Type: PreviewDone})
}

// EncodePreviewError writes a single error line.
func EncodePreviewError(w io.Writer, msg string) error {
	return json.NewEncoder(w).Encode(PreviewEvent{Type: PreviewError, Message: msg})
}

// DecodePreview reassembles what EncodePreview wrote. An error line yields
// its message as the error; a stream without done is reported as truncated.
func DecodePreview(lines [][]byte) (PreviewEvent, string, error) {
	var (
		meta PreviewEvent
		buf  bytes.Buffer
		done bool
	)
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev PreviewEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return meta, "", fmt.Errorf("preview output is not an event: %s", excerptBytes(line))
		}
		switch ev.Type {
		case PreviewMeta:
			meta = ev
		case PreviewHTML:
			raw, err := base64.StdEncoding.DecodeString(ev.B64)
			if err != nil {
				return meta, "", fmt.Errorf("preview html chunk is not base64: %w", err)
			}
			if buf.Len()+len(raw) > PreviewMaxHTMLBytes {
				return meta, "", fmt.Errorf("preview html exceeds %d MB", PreviewMaxHTMLBytes>>20)
			}
			buf.Write(raw)
		case PreviewDone:
			done = true
		case PreviewError:
			return meta, "", errors.New(ev.Message)
		}
	}
	if !done {
		return meta, "", errors.New("preview output ended before the page was complete")
	}
	return meta, buf.String(), nil
}

// DecodePreviewStream is DecodePreview over a reader, for tests and tools.
func DecodePreviewStream(r io.Reader) (PreviewEvent, string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var lines [][]byte
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		return PreviewEvent{}, "", err
	}
	return DecodePreview(lines)
}

func excerptBytes(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "…"
	}
	return string(b)
}

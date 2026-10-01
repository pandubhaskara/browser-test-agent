package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/pkg/spec"
)

// NetworkLog records every request a context makes, so the run can ship
// resources.json, so assert_requests has something to assert against, and so
// blocked requests are visible as blocked rather than as mysteriously missing.
type NetworkLog struct {
	mu        sync.Mutex
	runDir    string
	capture   bool
	resources []*spec.Resource
	// byReq keys in-flight requests by URL+method start time, which is the
	// best identity Playwright's Request interface gives from Go.
	byReq    map[playwright.Request]*spec.Resource
	started  map[playwright.Request]time.Time
	stepID   string
	payloads int
	// downloads collects finished downloads for assert_file_download.
	downloads []Download
	// masker scrubs payload files; set by the runner once the scope exists.
	Scrub func(string) string
}

// Download is a file the browser downloaded during the run.
type Download struct {
	SuggestedName string
	Path          string
	Size          int64
	MD5           string
	Failed        string
}

// NewNetworkLog creates a log writing payloads under runDir when capture is on.
func NewNetworkLog(runDir string, capture bool) *NetworkLog {
	return &NetworkLog{
		runDir:  runDir,
		capture: capture,
		byReq:   make(map[playwright.Request]*spec.Resource),
		started: make(map[playwright.Request]time.Time),
		Scrub:   func(s string) string { return s },
	}
}

// SetStep tags requests from now on with the step they belong to.
func (n *NetworkLog) SetStep(stepID string) {
	n.mu.Lock()
	n.stepID = stepID
	n.mu.Unlock()
}

// Attach hooks the context. Blocked patterns are routed to Abort and recorded;
// everything else is observed through the request/response events.
func (n *NetworkLog) Attach(ctx playwright.BrowserContext, blocked []string) error {
	for _, pattern := range blocked {
		re, err := chromeMatchPattern(pattern)
		if err != nil {
			return &InfraError{fmt.Errorf("blocked_requests pattern %q: %w", pattern, err)}
		}
		if err := ctx.Route(re, func(route playwright.Route) {
			req := route.Request()
			n.mu.Lock()
			n.resources = append(n.resources, &spec.Resource{
				URL: req.URL(), Method: req.Method(), Type: req.ResourceType(), Blocked: true, StepID: n.stepID,
			})
			n.mu.Unlock()
			_ = route.Abort("blockedbyclient")
		}); err != nil {
			return &InfraError{fmt.Errorf("route %q: %w", pattern, err)}
		}
	}

	ctx.OnRequest(func(req playwright.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		r := &spec.Resource{URL: req.URL(), Method: req.Method(), Type: req.ResourceType(), StepID: n.stepID}
		n.resources = append(n.resources, r)
		n.byReq[req] = r
		n.started[req] = time.Now()
	})
	ctx.OnResponse(func(resp playwright.Response) {
		req := resp.Request()
		n.mu.Lock()
		r := n.byReq[req]
		n.mu.Unlock()
		if r == nil {
			return
		}
		r.Status = resp.Status()
		if n.capture && isXHR(req.ResourceType()) {
			n.capturePayload(r, req, resp)
		}
	})
	ctx.OnRequestFinished(func(req playwright.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		if r := n.byReq[req]; r != nil {
			if t, ok := n.started[req]; ok {
				r.DurationMs = time.Since(t).Milliseconds()
			}
			delete(n.byReq, req)
			delete(n.started, req)
		}
	})
	ctx.OnRequestFailed(func(req playwright.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		if r := n.byReq[req]; r != nil {
			if err := req.Failure(); err != nil {
				r.Failed = err.Error()
			} else {
				r.Failed = "failed"
			}
			// A blocked request also fails; keep the blocked flag as the reason.
			if t, ok := n.started[req]; ok {
				r.DurationMs = time.Since(t).Milliseconds()
			}
			delete(n.byReq, req)
			delete(n.started, req)
		}
	})
	return nil
}

// WatchDownloads records downloads on a page. Called for the first page and
// every popup the runner switches to.
func (n *NetworkLog) WatchDownloads(page playwright.Page) {
	page.OnDownload(func(d playwright.Download) {
		entry := Download{SuggestedName: d.SuggestedFilename()}
		dir := filepath.Join(n.runDir, "downloads")
		if err := os.MkdirAll(dir, 0o750); err == nil {
			// The name on disk is the run's own numbering, not the browser's:
			// two downloads with the same suggested name must not collide.
			n.mu.Lock()
			idx := len(n.downloads) + 1
			n.mu.Unlock()
			dest := filepath.Join(dir, fmt.Sprintf("%03d-%s", idx, sanitizeName(d.SuggestedFilename())))
			if err := d.SaveAs(dest); err != nil {
				entry.Failed = err.Error()
			} else {
				entry.Path = dest
				entry.Size, entry.MD5 = fileSizeAndMD5(dest)
			}
		}
		if err := d.Failure(); err != nil && entry.Failed == "" {
			entry.Failed = err.Error()
		}
		n.mu.Lock()
		n.downloads = append(n.downloads, entry)
		n.mu.Unlock()
	})
}

// Downloads returns what was downloaded so far.
func (n *NetworkLog) Downloads() []Download {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Download(nil), n.downloads...)
}

// Resources returns a snapshot of everything observed.
func (n *NetworkLog) Resources() []*spec.Resource {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]*spec.Resource, len(n.resources))
	for i, r := range n.resources {
		c := *r
		out[i] = &c
	}
	return out
}

// Write saves resources.json. URLs are scrubbed: a secure variable in a query
// string is a secret in a log file.
func (n *NetworkLog) Write() (string, error) {
	res := n.Resources()
	for _, r := range res {
		r.URL = n.Scrub(r.URL)
		r.Failed = n.Scrub(r.Failed)
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(n.runDir, spec.ResourcesFile)
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return spec.ResourcesFile, nil
}

// capturePayload writes headers and body of an XHR/fetch exchange under
// payloads/. Bodies are capped: a payload capture that dumps a 50 MB response
// into the run folder is a disk problem, not evidence.
func (n *NetworkLog) capturePayload(r *spec.Resource, req playwright.Request, resp playwright.Response) {
	const maxBody = 1 << 20

	body, _ := resp.Body()
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	postData, _ := req.PostData()
	reqHeaders, _ := req.AllHeaders()
	respHeaders, _ := resp.AllHeaders()

	payload := map[string]any{
		"url":              n.Scrub(req.URL()),
		"method":           req.Method(),
		"status":           resp.Status(),
		"request_headers":  scrubMap(reqHeaders, n.Scrub),
		"request_body":     n.Scrub(postData),
		"response_headers": scrubMap(respHeaders, n.Scrub),
		"response_body":    n.Scrub(string(body)),
		"truncated":        len(body) == maxBody,
	}

	n.mu.Lock()
	n.payloads++
	idx := n.payloads
	n.mu.Unlock()

	dir := filepath.Join(n.runDir, spec.PayloadsDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	name := fmt.Sprintf("%d.json", idx)
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o640); err != nil {
		return
	}
	n.mu.Lock()
	r.Payload = spec.PayloadsDir + "/" + name
	n.mu.Unlock()
}

func scrubMap(m map[string]string, scrub func(string) string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		// Authorization and cookies are secrets whether or not a variable put
		// them there.
		switch strings.ToLower(k) {
		case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key":
			out[k] = "********"
		default:
			out[k] = scrub(v)
		}
	}
	return out
}

func isXHR(resourceType string) bool {
	return resourceType == "xhr" || resourceType == "fetch"
}

// chromeMatchPattern converts a Chrome extension match pattern
// (`<scheme>://<host>/<path>`, with `*` wildcards and `<all_urls>`) into a
// regexp Playwright's Route accepts. Chrome's `*` matches anything including
// `/`, which is not what Playwright's glob `*` means — hence a regexp, not a
// glob translation.
func chromeMatchPattern(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "<all_urls>" {
		return regexp.MustCompile(`^.*$`), nil
	}
	if !strings.Contains(pattern, "://") {
		// A bare host or substring: match it anywhere in the URL, which is what
		// a user typing "ads.example.com" means.
		return regexp.Compile(regexp.QuoteMeta(pattern))
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		if r == '*' {
			b.WriteString(".*")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func sanitizeName(name string) string {
	name = filepath.Base(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "download"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}

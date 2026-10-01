package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"browser-test-agent/pkg/spec"
)

// maxAPIBody caps how much of a response is read for assertions.
const maxAPIBody = 4 << 20

// runAPITest makes a plain HTTP request from the VM — the same network
// position as the browser, which is the point: an API that is only reachable
// from inside can be checked alongside the pages that call it.
func runAPITest(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.RunAPITestParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	method := strings.ToUpper(p.Method)
	if method == "" {
		method = http.MethodGet
	}
	timeout := c.Timeout
	if p.TimeoutSec > 0 {
		timeout = time.Duration(p.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(c.Ctx, timeout)
	defer cancel()

	var body io.Reader
	if p.Body != "" {
		body = strings.NewReader(p.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.URL, body)
	if err != nil {
		return Outcome{}, fmt.Errorf("invalid request: %w", err)
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("%s %s failed: %w", method, p.URL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	elapsed := time.Since(started)

	for i, a := range p.Assertions {
		if err := checkAPIAssertion(a, resp, string(respBody), elapsed); err != nil {
			return Outcome{}, fmt.Errorf("assertion %d: %w", i+1, err)
		}
	}
	return Outcome{}, nil
}

func checkAPIAssertion(a spec.APIAssertion, resp *http.Response, body string, elapsed time.Duration) error {
	var actual string
	switch a.Target {
	case "status_code":
		actual = strconv.Itoa(resp.StatusCode)
	case "body":
		actual = body
	case "header":
		actual = resp.Header.Get(a.Header)
	case "response_time_ms":
		actual = strconv.FormatInt(elapsed.Milliseconds(), 10)
	default:
		return fmt.Errorf("unknown target %q", a.Target)
	}

	switch a.Op {
	case "less_than", "greater_than":
		got, err1 := strconv.ParseFloat(actual, 64)
		want, err2 := strconv.ParseFloat(a.Value, 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("%s: %q and %q must both be numbers for %s", a.Target, actual, a.Value, a.Op)
		}
		if (a.Op == "less_than" && !(got < want)) || (a.Op == "greater_than" && !(got > want)) {
			return fmt.Errorf("%s is %s, expected %s %s", a.Target, actual, strings.ReplaceAll(a.Op, "_", " "), a.Value)
		}
		return nil
	}

	ok, err := compare(a.Op, actual, a.Value)
	if err != nil {
		return err
	}
	if !ok {
		shown := actual
		if a.Target == "body" {
			shown = excerpt(actual)
		}
		return fmt.Errorf("%s is %q, expected it to %s %q", a.Target, shown, opLabel(a.Op), a.Value)
	}
	return nil
}

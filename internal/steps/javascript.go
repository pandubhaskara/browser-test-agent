package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"browser-test-agent/pkg/spec"
)

// evaluate runs user JavaScript in the page. A bare expression and a function
// body with `return` are both accepted, because both are what people paste.
func evaluate(c *Context, code string) (any, error) {
	code = strings.TrimSpace(code)
	expr := code
	if strings.Contains(code, "return ") || strings.Contains(code, ";") || strings.Contains(code, "\n") {
		expr = "(() => { " + code + " })()"
	}
	result, err := c.Page.Evaluate(expr)
	if err != nil {
		return nil, fmt.Errorf("JavaScript failed: %w", err)
	}
	return result, nil
}

// truthy follows JavaScript's own rules as they survive JSON: null, false, 0,
// "" and empty are falsy; everything else passes.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t != ""
	case []any:
		return true
	case map[string]any:
		return true
	}
	return true
}

func assertFromJS(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.AssertFromJSParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	result, err := evaluate(c, p.Code)
	if err != nil {
		return Outcome{}, err
	}
	if !truthy(result) {
		return Outcome{}, fmt.Errorf("JavaScript assertion returned %s", jsRepr(result))
	}
	return Outcome{}, nil
}

func extractFromJS(c *Context, _ spec.Step, raw json.RawMessage) (Outcome, error) {
	var p spec.ExtractFromJSParams
	if err := decode(raw, &p); err != nil {
		return Outcome{}, err
	}
	result, err := evaluate(c, p.Code)
	if err != nil {
		return Outcome{}, err
	}
	value := jsString(result)
	c.Scope.Set(p.Name, value, p.Secure)
	return Outcome{Extracted: extracted(p.Name, value, p.Secure)}, nil
}

// jsString renders an evaluation result as the variable's text: strings as
// themselves, everything else as JSON.
func jsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func jsRepr(v any) string {
	if v == nil {
		return "null"
	}
	return excerpt(jsString(v))
}

// Package vars is the variable language shared by the backend and the runner:
// `{{ NAME }}` references with optional filters, builtin generators, and the
// masking of secure values.
//
// The backend uses it for the "preview" endpoint (so a user sees what a step
// will send before saving) and to resolve global variables; the runner uses it
// at run start for local variables and during the run for extracted ones. One
// package, so the two never disagree about what `{{ date(1d, YYYY-MM-DD) }}`
// means.
package vars

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mask is what a secure value becomes in every event, error and stored param.
const Mask = "********"

// refRe matches `{{ NAME }}` and `{{ NAME | filter }}`, with optional builtin
// arguments: `{{ numeric(6) }}`, `{{ date(-1d, YYYY-MM-DD) | urlEncode }}`.
var refRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)(?:\s*\(([^)]*)\))?\s*(?:\|\s*([A-Za-z][A-Za-z0-9]*)\s*)?\}\}`)

// Scope holds the variables available while resolving.
type Scope struct {
	values map[string]Value
	// TestID and RunID back the public-id and result-id builtins.
	TestID string
	RunID  string
	// Now is the clock the date/timestamp builtins read. Fixed at scope
	// creation so every reference in one run agrees on the time.
	Now time.Time
	// mask collects every secure value seen, so callers can scrub output.
	mask *Masker
}

// Value is a variable's resolved value.
type Value struct {
	V      string
	Secure bool
}

// NewScope creates an empty scope with the run's identifiers.
func NewScope(testID, runID string, now time.Time, m *Masker) *Scope {
	if m == nil {
		m = NewMasker()
	}
	return &Scope{values: map[string]Value{}, TestID: testID, RunID: runID, Now: now, mask: m}
}

// Set binds a variable. A secure value is also registered with the masker.
func (s *Scope) Set(name, value string, secure bool) {
	s.values[name] = Value{V: value, Secure: secure}
	if secure && value != "" {
		s.mask.Add(value)
	}
}

// Get returns a bound variable.
func (s *Scope) Get(name string) (Value, bool) {
	v, ok := s.values[name]
	return v, ok
}

// Names lists bound variables, for previews and error messages.
func (s *Scope) Names() []string {
	out := make([]string, 0, len(s.values))
	for k := range s.values {
		out = append(out, k)
	}
	return out
}

// Masker is the scope's masker, for output that must scrub secure values.
func (s *Scope) Masker() *Masker { return s.mask }

// ErrUnknownVariable is wrapped into a resolution error naming the variable.
var ErrUnknownVariable = errors.New("unknown variable")

// Resolve replaces every reference in text. It also reports whether any
// referenced value was secure, which is what tells a type_text step to skip
// its screenshot.
func (s *Scope) Resolve(text string) (out string, usedSecure bool, err error) {
	var firstErr error
	out = refRe.ReplaceAllStringFunc(text, func(match string) string {
		m := refRe.FindStringSubmatch(match)
		name, args, filter := m[1], m[2], m[3]

		val, secure, rerr := s.lookup(name, args)
		if rerr != nil {
			if firstErr == nil {
				firstErr = rerr
			}
			return match
		}
		if secure {
			usedSecure = true
		}
		if filter != "" {
			filtered, ferr := applyFilter(filter, val)
			if ferr != nil {
				if firstErr == nil {
					firstErr = ferr
				}
				return match
			}
			val = filtered
			// A filtered secure value is still the secret in another spelling.
			if secure {
				s.mask.Add(val)
			}
		}
		return val
	})
	return out, usedSecure, firstErr
}

// MustResolve is Resolve for callers that treat an unresolved reference as a
// hard error, returning the original text alongside so it can be reported.
func (s *Scope) MustResolve(text string) (string, bool, error) {
	out, secure, err := s.Resolve(text)
	if err != nil {
		return text, secure, err
	}
	return out, secure, nil
}

// lookup resolves one reference: a bound variable first (so a user can shadow a
// builtin name deliberately), then a builtin.
func (s *Scope) lookup(name, args string) (string, bool, error) {
	if v, ok := s.values[name]; ok {
		return v.V, v.Secure, nil
	}
	val, ok, err := s.builtin(name, args)
	if err != nil {
		return "", false, fmt.Errorf("{{ %s }}: %w", name, err)
	}
	if !ok {
		return "", false, fmt.Errorf("{{ %s }}: %w", name, ErrUnknownVariable)
	}
	return val, false, nil
}

// Builtins lists the generator names, for the UI's autocomplete.
var Builtins = []string{"numeric", "alphabetic", "alphanumeric", "date", "timestamp", "uuid", "public-id", "result-id"}

// Filters lists the filter names.
var Filters = []string{"urlEncode", "urlDecode", "base64Encode", "base64Decode"}

func (s *Scope) builtin(name, args string) (string, bool, error) {
	switch name {
	case "numeric":
		n, err := intArg(args, 1, 64)
		if err != nil {
			return "", true, err
		}
		return randomString(n, "0123456789"), true, nil
	case "alphabetic":
		n, err := intArg(args, 1, 64)
		if err != nil {
			return "", true, err
		}
		return randomString(n, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"), true, nil
	case "alphanumeric":
		n, err := intArg(args, 1, 64)
		if err != nil {
			return "", true, err
		}
		return randomString(n, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"), true, nil
	case "uuid":
		return uuid4(), true, nil
	case "public-id":
		return s.TestID, true, nil
	case "result-id":
		return s.RunID, true, nil
	case "date":
		v, err := dateBuiltin(s.Now, args)
		return v, true, err
	case "timestamp":
		v, err := timestampBuiltin(s.Now, args)
		return v, true, err
	}
	return "", false, nil
}

func intArg(args string, min, max int) (int, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return 0, errors.New("needs a length argument, e.g. numeric(6)")
	}
	n, err := strconv.Atoi(args)
	if err != nil {
		return 0, fmt.Errorf("length %q is not a number", args)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("length must be between %d and %d", min, max)
	}
	return n, nil
}

// dateBuiltin is `date(<offset>, <format>)`: an offset like `-1d`, `2h`, `30m`
// (or `0`), and a moment-style format such as YYYY-MM-DD or ISO for RFC3339.
func dateBuiltin(now time.Time, args string) (string, error) {
	offsetArg, format, err := splitTwo(args, "date(-1d, YYYY-MM-DD)")
	if err != nil {
		return "", err
	}
	offset, err := parseOffset(offsetArg)
	if err != nil {
		return "", err
	}
	t := now.Add(offset).UTC()
	return formatDate(t, format), nil
}

// timestampBuiltin is `timestamp(<n>, <unit>)`: Unix seconds (unit s, the
// default) or milliseconds (unit ms), offset by n units.
func timestampBuiltin(now time.Time, args string) (string, error) {
	parts := strings.Split(args, ",")
	n := 0
	unit := "s"
	if len(parts) >= 1 && strings.TrimSpace(parts[0]) != "" {
		v, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return "", fmt.Errorf("offset %q is not a number", parts[0])
		}
		n = v
	}
	if len(parts) >= 2 {
		unit = strings.TrimSpace(parts[1])
	}
	switch unit {
	case "s":
		return strconv.FormatInt(now.Add(time.Duration(n)*time.Second).Unix(), 10), nil
	case "ms":
		return strconv.FormatInt(now.Add(time.Duration(n)*time.Millisecond).UnixMilli(), 10), nil
	case "m":
		return strconv.FormatInt(now.Add(time.Duration(n)*time.Minute).Unix(), 10), nil
	case "h":
		return strconv.FormatInt(now.Add(time.Duration(n)*time.Hour).Unix(), 10), nil
	case "d":
		return strconv.FormatInt(now.Add(time.Duration(n)*24*time.Hour).Unix(), 10), nil
	}
	return "", fmt.Errorf("unit %q must be s, ms, m, h or d", unit)
}

func splitTwo(args, example string) (string, string, error) {
	parts := strings.SplitN(args, ",", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("needs two arguments, e.g. %s", example)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// parseOffset reads "-1d", "2h", "30m", "45s", "0".
func parseOffset(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "0" || s == "" {
		return 0, nil
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return 0, fmt.Errorf("offset %q must look like -1d, 2h, 30m or 45s", s)
	}
	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 's':
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("offset %q must end in d, h, m or s", s)
}

// formatDate renders a moment-style format. ISO is RFC3339; otherwise the
// tokens YYYY YY MM DD HH mm ss SSS are replaced and everything else is kept.
func formatDate(t time.Time, format string) string {
	if strings.EqualFold(format, "ISO") {
		return t.Format(time.RFC3339)
	}
	r := strings.NewReplacer(
		"YYYY", t.Format("2006"),
		"YY", t.Format("06"),
		"MM", t.Format("01"),
		"DD", t.Format("02"),
		"HH", t.Format("15"),
		"mm", t.Format("04"),
		"ss", t.Format("05"),
		"SSS", fmt.Sprintf("%03d", t.Nanosecond()/1e6),
	)
	return r.Replace(format)
}

func applyFilter(name, val string) (string, error) {
	switch name {
	case "urlEncode":
		return url.QueryEscape(val), nil
	case "urlDecode":
		out, err := url.QueryUnescape(val)
		if err != nil {
			return "", fmt.Errorf("urlDecode: %w", err)
		}
		return out, nil
	case "base64Encode":
		return base64.StdEncoding.EncodeToString([]byte(val)), nil
	case "base64Decode":
		out, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			return "", fmt.Errorf("base64Decode: %w", err)
		}
		return string(out), nil
	}
	return "", fmt.Errorf("unknown filter %q (filters are case-sensitive: %s)", name, strings.Join(Filters, ", "))
}

func randomString(n int, alphabet string) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failing means the OS entropy source is gone; a
			// predictable value is still better than a crash mid-run.
			b[i] = alphabet[i%len(alphabet)]
			continue
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b)
}

func uuid4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// References lists the variable names a text references, without resolving.
// The backend uses it to warn about references no variable will satisfy.
func References(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// IsBuiltin reports whether name is a generator rather than a variable.
func IsBuiltin(name string) bool {
	for _, b := range Builtins {
		if b == name {
			return true
		}
	}
	return false
}

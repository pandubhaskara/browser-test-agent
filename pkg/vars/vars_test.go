package vars

import (
	"strings"
	"testing"
	"time"
)

func scope() *Scope {
	now := time.Date(2026, 9, 29, 10, 30, 0, 0, time.UTC)
	return NewScope("test-abc", "run-123", now, NewMasker())
}

func TestResolveVariablesAndFilters(t *testing.T) {
	s := scope()
	s.Set("USER", "alice", false)
	s.Set("QUERY", "a b&c", false)

	out, secure, err := s.Resolve("hello {{ USER }}, q={{ QUERY | urlEncode }}")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello alice, q=a+b%26c" {
		t.Errorf("got %q", out)
	}
	if secure {
		t.Error("no secure value was used")
	}
}

func TestResolveReportsSecureUse(t *testing.T) {
	s := scope()
	s.Set("PASSWORD", "example-value", true)
	out, secure, err := s.Resolve("pw={{ PASSWORD }}")
	if err != nil {
		t.Fatal(err)
	}
	if out != "pw=example-value" {
		t.Errorf("got %q", out)
	}
	if !secure {
		t.Error("a secure value was used but not reported; the screenshot would not be skipped")
	}
	if got := s.Masker().Mask(out); got != "pw="+Mask {
		t.Errorf("masker did not learn the secure value: %q", got)
	}
}

// A filtered secret is still the secret in another spelling.
func TestFilteredSecureValueIsMasked(t *testing.T) {
	s := scope()
	s.Set("TOKEN", "example-value-2", true)
	out, _, err := s.Resolve("{{ TOKEN | base64Encode }}")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Masker().Mask("x " + out + " y"); got != "x "+Mask+" y" {
		t.Errorf("base64 form of the secret leaked: %q", got)
	}
}

func TestUnknownVariableIsAnError(t *testing.T) {
	s := scope()
	_, _, err := s.Resolve("{{ NOPE }}")
	if err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("err = %v, want it to name the variable", err)
	}
}

func TestBuiltins(t *testing.T) {
	s := scope()
	cases := map[string]func(string) bool{
		"{{ numeric(6) }}":                 func(v string) bool { return len(v) == 6 && strings.Trim(v, "0123456789") == "" },
		"{{ alphabetic(8) }}":              func(v string) bool { return len(v) == 8 && strings.ToLower(v) != v || len(v) == 8 },
		"{{ alphanumeric(10) }}":           func(v string) bool { return len(v) == 10 },
		"{{ uuid }}":                       func(v string) bool { return len(v) == 36 && v[14] == '4' },
		"{{ public-id }}":                  func(v string) bool { return v == "test-abc" },
		"{{ result-id }}":                  func(v string) bool { return v == "run-123" },
		"{{ date(0, YYYY-MM-DD) }}":        func(v string) bool { return v == "2026-09-29" },
		"{{ date(-1d, YYYY-MM-DD) }}":      func(v string) bool { return v == "2026-09-28" },
		"{{ date(2h, YYYY-MM-DD HH:mm) }}": func(v string) bool { return v == "2026-09-29 12:30" },
		"{{ date(0, ISO) }}":               func(v string) bool { return v == "2026-09-29T10:30:00Z" },
		"{{ timestamp(0, s) }}":            func(v string) bool { return v == "1790677800" },
		"{{ timestamp(0, ms) }}":           func(v string) bool { return v == "1790677800000" },
		"{{ timestamp(-1, h) }}":           func(v string) bool { return v == "1790674200" },
	}
	for in, check := range cases {
		out, _, err := s.Resolve(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !check(out) {
			t.Errorf("%s resolved to %q", in, out)
		}
	}
}

func TestBuiltinArgumentErrors(t *testing.T) {
	s := scope()
	for _, in := range []string{"{{ numeric() }}", "{{ numeric(0) }}", "{{ numeric(999) }}", "{{ date(1x, YYYY) }}", "{{ timestamp(0, weeks) }}", "{{ USER | shout }}"} {
		s.Set("USER", "x", false)
		if _, _, err := s.Resolve(in); err == nil {
			t.Errorf("%s: expected an error", in)
		}
	}
}

// A bound variable wins over a builtin of the same name, so a user can shadow
// one deliberately.
func TestBoundVariableShadowsBuiltin(t *testing.T) {
	s := scope()
	s.Set("uuid", "fixed", false)
	out, _, _ := s.Resolve("{{ uuid }}")
	if out != "fixed" {
		t.Errorf("got %q", out)
	}
}

func TestReferences(t *testing.T) {
	refs := References("{{ A }} and {{ B | urlEncode }} and {{ A }} and {{ numeric(3) }}")
	want := []string{"A", "B", "numeric"}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("refs = %v, want %v", refs, want)
		}
	}
}

func TestMasker(t *testing.T) {
	m := NewMasker()
	m.Add("secret")
	m.Add("se") // too short: masking "se" would eat half the alphabet
	m.Add("secret-and-more")
	got := m.Mask("the secret-and-more and the secret and se")
	if got != "the "+Mask+" and the "+Mask+" and se" {
		t.Errorf("got %q", got)
	}
	if m.Len() != 2 {
		t.Errorf("registered %d values, want 2", m.Len())
	}
}

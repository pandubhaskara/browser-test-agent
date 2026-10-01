package browser

import "regexp"

// MatchPattern is chromeMatchPattern for callers outside the package
// (assert_requests uses the same pattern language as blocked_requests).
func MatchPattern(pattern string) (*regexp.Regexp, error) {
	return chromeMatchPattern(pattern)
}

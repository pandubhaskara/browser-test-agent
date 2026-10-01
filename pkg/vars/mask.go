package vars

import (
	"sort"
	"strings"
	"sync"
)

// Masker replaces secure values with Mask wherever they appear in text. It is
// the one place secrets are scrubbed: the runner's event writer runs every
// line through it, and so do error messages and stored step params.
//
// It matches by value, not by variable name, so a secret that reached the
// output through a filter, a concatenation or an error message from the
// browser is still caught — as long as it was registered.
type Masker struct {
	mu     sync.RWMutex
	values []string
}

// NewMasker returns an empty masker.
func NewMasker() *Masker { return &Masker{} }

// minMaskLen keeps trivially short values from being masked: masking "1"
// would replace every digit 1 in every line, and a one-character secret is not
// a secret worth that.
const minMaskLen = 3

// Add registers a value to be masked. Duplicates and short values are ignored.
func (m *Masker) Add(value string) {
	if len(value) < minMaskLen {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.values {
		if v == value {
			return
		}
	}
	m.values = append(m.values, value)
	// Longest first, so a value that contains another is masked whole rather
	// than leaving fragments of the longer one around a mask of the shorter.
	sort.Slice(m.values, func(i, j int) bool { return len(m.values[i]) > len(m.values[j]) })
}

// Mask scrubs text.
func (m *Masker) Mask(text string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.values {
		text = strings.ReplaceAll(text, v, Mask)
	}
	return text
}

// Len is how many values are registered, for tests and the doctor report.
func (m *Masker) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.values)
}

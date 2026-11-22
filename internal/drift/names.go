package drift

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const separators = "-_./"

// CompilePattern compiles p case-insensitively so "it-77" matches `IT-\d+`.
func CompilePattern(p string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return nil, fmt.Errorf("drift: invalid ticket pattern %q: %w", p, err)
	}
	return re, nil
}

// TicketIDs returns every match of re in branch, upper-cased and
// de-duplicated, in order of first appearance. It returns nil when none match.
func TicketIDs(re *regexp.Regexp, branch string) []string {
	var ids []string
	for _, m := range re.FindAllString(branch, -1) {
		m = strings.ToUpper(m)
		if !slices.Contains(ids, m) {
			ids = append(ids, m)
		}
	}
	return ids
}

// StripEnvSuffix removes one trailing <sep><word> (word compared
// case-insensitively). ok is false when nothing was stripped or the result
// would be empty.
func StripEnvSuffix(branch string, words []string) (stripped string, ok bool) {
	for _, w := range words {
		if len(branch) <= len(w)+1 {
			continue
		}
		cut := len(branch) - len(w) - 1
		if strings.IndexByte(separators, branch[cut]) >= 0 && strings.EqualFold(branch[cut+1:], w) {
			return branch[:cut], true
		}
	}
	return branch, false
}

// SiblingCandidates lists branch names that may be the same work on another
// environment branch: the base name (suffix stripped) followed by the base
// plus every separator and word. The branch itself is excluded.
func SiblingCandidates(branch string, words []string) []string {
	base, _ := StripEnvSuffix(branch, words)
	var out []string
	add := func(s string) {
		if s != branch && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	add(base)
	for _, sep := range separators {
		for _, w := range words {
			add(base + string(sep) + w)
		}
	}
	return out
}

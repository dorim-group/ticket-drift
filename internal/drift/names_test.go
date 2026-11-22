package drift

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTicketIDs(t *testing.T) {
	re, err := CompilePattern(`IT-\d+`)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"IT-12345/some-feature":     {"IT-12345"},
		"IT-123456":                 {"IT-123456"},
		"IT-12321-sdsad":            {"IT-12321"},
		"it-77/lowercase":           {"IT-77"},
		"qa/IT-9123-IT-8982-in-dev": {"IT-9123", "IT-8982"},
		"IT-5/x-IT-5":               {"IT-5"},
		"esf-hotfix":                nil,
		"feat/visionai-task-id":     nil,
	}
	for branch, want := range cases {
		if got := TicketIDs(re, branch); !reflect.DeepEqual(got, want) {
			t.Errorf("TicketIDs(%q) = %v, want %v", branch, got, want)
		}
	}
}

func TestCompilePatternInvalid(t *testing.T) {
	_, err := CompilePattern(`(`)
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
	if !strings.HasPrefix(err.Error(), "drift: ") {
		t.Errorf("err = %q, want the drift: prefix", err)
	}
}

func TestStripEnvSuffix(t *testing.T) {
	words := []string{"main", "dev", "stage", "prod"}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"IT-10249/responsible-managers-dev", "IT-10249/responsible-managers", true},
		{"fix.price_lists_dev", "fix.price_lists", true},
		{"IT-3170.prod", "IT-3170", true},
		{"IT-1/x-DEV", "IT-1/x", true},
		{"IT-10249/responsible-managers", "IT-10249/responsible-managers", false},
		{"IT-1/devtools", "IT-1/devtools", false},
		{"dev", "dev", false},
		{"-dev", "-dev", false},
	}
	for _, c := range cases {
		got, ok := StripEnvSuffix(c.in, words)
		if got != c.want || ok != c.ok {
			t.Errorf("StripEnvSuffix(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSiblingCandidates(t *testing.T) {
	words := []string{"main", "dev"}

	got := SiblingCandidates("IT-1/x-dev", words)
	if got[0] != "IT-1/x" {
		t.Errorf("first = %q, want IT-1/x", got[0])
	}
	for _, w := range []string{"IT-1/x-main", "IT-1/x_main", "IT-1/x.main", "IT-1/x/main"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if slices.Contains(got, "IT-1/x-dev") {
		t.Errorf("must not contain the branch itself: %v", got)
	}
	if len(got) != 1+4*2-1 {
		t.Errorf("len = %d, want 8: %v", len(got), got)
	}

	got = SiblingCandidates("IT-1/x", words)
	if slices.Contains(got, "IT-1/x") {
		t.Errorf("must not contain the branch itself: %v", got)
	}
	for _, w := range []string{"IT-1/x-dev", "IT-1/x-main"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if len(got) != 8 {
		t.Errorf("len = %d, want 8: %v", len(got), got)
	}
}

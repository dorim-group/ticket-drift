package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestUsageListsHelp(t *testing.T) {
	if !strings.Contains(usageText, "\n  help ") {
		t.Errorf("usage lacks the help command:\n%s", usageText)
	}
}

func TestResolveVersion(t *testing.T) {
	info := func(v string, ok bool) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			if !ok {
				return nil, false
			}
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	for _, tc := range []struct {
		name, stamped string
		read          func() (*debug.BuildInfo, bool)
		want          string
	}{
		{"ldflags win", "v1.2.0", info("v9.9.9", true), "v1.2.0"},
		{"go install @vX", "dev", info("v0.2.0", true), "v0.2.0"},
		{"local build", "dev", info("(devel)", true), "dev"},
		{"no module version", "dev", info("", true), "dev"},
		{"no build info", "dev", info("", false), "dev"},
	} {
		if got := resolveVersion(tc.stamped, tc.read); got != tc.want {
			t.Errorf("%s: resolveVersion = %q, want %q", tc.name, got, tc.want)
		}
	}
}

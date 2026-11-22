package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

// The report is promised on failure too, including a failure to build the
// GitHub App client: here, a key that is valid base64 but not an RSA key.
func TestSyncCommandWritesReportWhenAppClientFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	cfg := config{
		Owner: "o", Repo: "r", Base: "main", Targets: []string{"dev", "stage"},
		NoReset: true, OutJSON: path,
		AppID: "1", AppInstallationID: "2", AppKey: []byte("not a key"),
	}
	err := syncCommand(cfg, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("err = %v, want the private key error", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"ticket_drift_version": version, "owner": "o", "repo": "r", "base": "main", "base_sha": "",
		"dry_run": false, "no_reset": true, "targets": []any{}, "groups": []any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("report = %v\nwant %v", got, want)
	}
}

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

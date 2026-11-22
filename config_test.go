package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"
)

func baseEnv() map[string]string {
	return map[string]string{
		"CIRCLE_PROJECT_USERNAME": "acme",
		"CIRCLE_PROJECT_REPONAME": "shop",
		"GITHUB_TOKEN":            "gh-secret",
		"DISCORD_WEBHOOK_URL":     "https://discord.example/hook",
		"CLICKUP_API_TOKEN":       "cu-secret",
		"CLICKUP_TEAM_ID":         "42",
	}
}

func parse(t *testing.T, env map[string]string, files map[string]string, args ...string) (config, error) {
	t.Helper()
	getenv := func(k string) string { return env[k] }
	readFile := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no such file")
	}
	return parseConfig(args, getenv, readFile)
}

func TestConfigDefaultsFromCircleEnv(t *testing.T) {
	cfg, err := parse(t, baseEnv(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Owner != "acme" || cfg.Repo != "shop" {
		t.Errorf("owner/repo = %q/%q", cfg.Owner, cfg.Repo)
	}
	if cfg.Base != "main" {
		t.Errorf("base = %q", cfg.Base)
	}
	if !reflect.DeepEqual(cfg.Targets, []string{"dev", "stage"}) {
		t.Errorf("targets = %v", cfg.Targets)
	}
	if !cfg.Pattern.MatchString("it-1") {
		t.Error("default pattern should match it-1")
	}
}

func TestConfigFlagsOverrideEnv(t *testing.T) {
	cfg, err := parse(t, baseEnv(), nil, "-owner", "x", "-repo", "y", "-targets", "dev, qa ,")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Owner != "x" || cfg.Repo != "y" {
		t.Errorf("owner/repo = %q/%q", cfg.Owner, cfg.Repo)
	}
	if !reflect.DeepEqual(cfg.Targets, []string{"dev", "qa"}) {
		t.Errorf("targets = %v", cfg.Targets)
	}
}

func TestConfigRequiresClickUpEvenInDryRun(t *testing.T) {
	env := baseEnv()
	delete(env, "CLICKUP_TEAM_ID")
	_, err := parse(t, env, nil, "-dry-run")
	if err == nil || !strings.Contains(err.Error(), "CLICKUP_TEAM_ID") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigDiscordOptionalOnlyInDryRun(t *testing.T) {
	env := baseEnv()
	delete(env, "DISCORD_WEBHOOK_URL")
	_, err := parse(t, env, nil)
	if err == nil || !strings.Contains(err.Error(), "DISCORD_WEBHOOK_URL") {
		t.Fatalf("err = %v", err)
	}
	cfg, err := parse(t, env, nil, "-dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DryRun {
		t.Error("DryRun not set")
	}
}

func TestConfigGitHubTokenOrApp(t *testing.T) {
	env := baseEnv()
	delete(env, "GITHUB_TOKEN")
	if _, err := parse(t, env, nil); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("neither: err = %v", err)
	}

	cfg, err := parse(t, baseEnv(), nil)
	if err != nil || cfg.GitHubToken != "gh-secret" {
		t.Fatalf("token only: %v %q", err, cfg.GitHubToken)
	}

	pem := "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n"
	app := baseEnv()
	delete(app, "GITHUB_TOKEN")
	app["TICKET_DRIFT_APP_ID"] = "1"
	app["TICKET_DRIFT_APP_INSTALLATION_ID"] = "2"
	app["TICKET_DRIFT_APP_PRIVATE_KEY"] = base64.StdEncoding.EncodeToString([]byte(pem))
	cfg, err = parse(t, app, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.AppKey) != pem || cfg.AppID != "1" || cfg.AppInstallationID != "2" {
		t.Errorf("app config = %+v", cfg)
	}

	// A token wins when both are present.
	both := baseEnv()
	both["TICKET_DRIFT_APP_ID"] = "1"
	both["TICKET_DRIFT_APP_INSTALLATION_ID"] = "2"
	both["TICKET_DRIFT_APP_PRIVATE_KEY"] = base64.StdEncoding.EncodeToString([]byte(pem))
	cfg, err = parse(t, both, nil)
	if err != nil || cfg.GitHubToken != "gh-secret" {
		t.Fatalf("both: %v", err)
	}

	partial := baseEnv()
	delete(partial, "GITHUB_TOKEN")
	partial["TICKET_DRIFT_APP_ID"] = "1"
	partial["TICKET_DRIFT_APP_PRIVATE_KEY"] = base64.StdEncoding.EncodeToString([]byte(pem))
	_, err = parse(t, partial, nil)
	if err == nil || !strings.Contains(err.Error(), "TICKET_DRIFT_APP_INSTALLATION_ID") {
		t.Fatalf("partial: err = %v", err)
	}

	bad := baseEnv()
	delete(bad, "GITHUB_TOKEN")
	bad["TICKET_DRIFT_APP_ID"] = "1"
	bad["TICKET_DRIFT_APP_INSTALLATION_ID"] = "2"
	bad["TICKET_DRIFT_APP_PRIVATE_KEY"] = "not base64 !!"
	_, err = parse(t, bad, nil)
	if err == nil || !strings.Contains(err.Error(), "TICKET_DRIFT_APP_PRIVATE_KEY") || strings.Contains(err.Error(), "not base64") {
		t.Fatalf("bad base64: err = %v", err)
	}
}

func TestConfigUsersEmptyIsFine(t *testing.T) {
	for _, v := range []string{"", "  "} {
		env := baseEnv()
		env["TICKET_DRIFT_USERS"] = v
		cfg, err := parse(t, env, nil)
		if err != nil || len(cfg.Users) != 0 || cfg.Users == nil {
			t.Errorf("%q: users=%v err=%v", v, cfg.Users, err)
		}
	}
	cfg, err := parse(t, baseEnv(), nil)
	if err != nil || len(cfg.Users) != 0 {
		t.Errorf("unset: users=%v err=%v", cfg.Users, err)
	}
}

func TestConfigUsersInvalidJSON(t *testing.T) {
	env := baseEnv()
	env["TICKET_DRIFT_USERS"] = "{oops"
	_, err := parse(t, env, nil)
	if err == nil || !strings.Contains(err.Error(), "TICKET_DRIFT_USERS") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigUsersLowercased(t *testing.T) {
	env := baseEnv()
	env["TICKET_DRIFT_USERS"] = `{"Bob":"111111111111111111"}`
	cfg, err := parse(t, env, nil)
	if err != nil || cfg.Users["bob"] != "111111111111111111" {
		t.Fatalf("users=%v err=%v", cfg.Users, err)
	}
}

func TestConfigUsersFileOverridesEnv(t *testing.T) {
	env := baseEnv()
	env["TICKET_DRIFT_USERS"] = `{"env":"111111111111111111"}`
	cfg, err := parse(t, env, map[string]string{"u.json": `{"File":"222222222222222222"}`}, "-users", "u.json")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Users, map[string]string{"file": "222222222222222222"}) {
		t.Errorf("users = %v", cfg.Users)
	}
	if _, err := parse(t, env, nil, "-users", "missing.json"); err == nil {
		t.Error("unreadable users file should be an error")
	}
}

func TestConfigBaseAmongTargets(t *testing.T) {
	if _, err := parse(t, baseEnv(), nil, "-targets", "main,dev"); err == nil {
		t.Fatal("want error")
	}
}

func TestConfigBadPattern(t *testing.T) {
	if _, err := parse(t, baseEnv(), nil, "-ticket-pattern", "("); err == nil {
		t.Fatal("want error")
	}
}

func TestConfigHelp(t *testing.T) {
	env := baseEnv()
	_, err := parse(t, env, nil, "-h")
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v", err)
	}
	var he helpError
	if !errors.As(err, &he) {
		t.Fatalf("not a helpError: %T", err)
	}
	if !strings.HasPrefix(he.Usage, "usage: ticket-drift sync [flags]\n\n") {
		t.Errorf("usage = %q", he.Usage)
	}
	for _, want := range []string{"-dry-run", "-targets", "-ticket-pattern"} {
		if !strings.Contains(he.Usage, want) {
			t.Errorf("usage lacks %s", want)
		}
	}
	for _, k := range []string{"GITHUB_TOKEN", "DISCORD_WEBHOOK_URL", "CLICKUP_API_TOKEN"} {
		if strings.Contains(he.Usage, env[k]) {
			t.Errorf("usage leaks %s", k)
		}
	}
}

func TestConfigRejectsPositionalArgs(t *testing.T) {
	_, err := parse(t, baseEnv(), nil, "foo")
	if err == nil || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigWhitespaceSecretIsMissing(t *testing.T) {
	env := baseEnv()
	env["CLICKUP_API_TOKEN"] = "  "
	_, err := parse(t, env, nil)
	if err == nil || !strings.Contains(err.Error(), "CLICKUP_API_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigUsersCaseCollision(t *testing.T) {
	env := baseEnv()
	env["TICKET_DRIFT_USERS"] = `{"Bob":"111111111111111111","bob":"222222222222222222"}`
	_, err := parse(t, env, nil)
	if err == nil || !strings.Contains(err.Error(), `"Bob"`) || !strings.Contains(err.Error(), `"bob"`) {
		t.Fatalf("err = %v", err)
	}
}

// Discord rejects the whole message for a non-snowflake in allowed_mentions,
// which would block every reset; catch it before any network use.
func TestConfigUsersRejectsNonSnowflake(t *testing.T) {
	env := baseEnv()
	env["TICKET_DRIFT_USERS"] = `{"bob":"@bob"}`
	_, err := parse(t, env, nil)
	if err == nil || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("err = %v, want an error naming bob", err)
	}
}

func TestConfigTargetsDeduplicated(t *testing.T) {
	cfg, err := parse(t, baseEnv(), nil, "-targets", "dev,dev,stage")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Targets, []string{"dev", "stage"}) {
		t.Errorf("targets = %v, want [dev stage]", cfg.Targets)
	}
}

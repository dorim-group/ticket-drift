package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/dorim-group/ticket-drift/internal/drift"
)

type config struct {
	Owner, Repo string
	Base        string
	Targets     []string
	Pattern     *regexp.Regexp
	Users       map[string]string // keys lower-cased
	OutJSON     string
	DryRun      bool
	NoReset     bool

	GitHubToken                 string
	AppID, AppInstallationID    string
	AppKey                      []byte // PEM, already base64-decoded
	DiscordWebhook              string
	ClickUpToken, ClickUpTeamID string
}

// parseConfig is pure: the environment and files are reached only through
// getenv and readFile, and nothing is printed. Every failure is returned
// before any network use, and never contains a secret's value.
func parseConfig(args []string, getenv func(string) string, readFile func(string) ([]byte, error)) (config, error) {
	var (
		cfg              config
		targets, pattern string
		usersFile        string
	)
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	var out bytes.Buffer
	fs.SetOutput(&out)
	fs.StringVar(&cfg.Owner, "owner", getenv("CIRCLE_PROJECT_USERNAME"), "repository owner")
	fs.StringVar(&cfg.Repo, "repo", getenv("CIRCLE_PROJECT_REPONAME"), "repository name")
	fs.StringVar(&cfg.Base, "base", "main", "base branch")
	fs.StringVar(&targets, "targets", "dev,stage", "comma-separated target branches")
	fs.StringVar(&pattern, "ticket-pattern", `IT-\d+`, "ticket id regular expression")
	fs.StringVar(&usersFile, "users", "", "JSON file mapping GitHub logins to ClickUp user ids")
	fs.StringVar(&cfg.OutJSON, "out-json", "", "write the JSON report to this path")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "do not post to Discord")
	fs.BoolVar(&cfg.NoReset, "no-reset", false, "do not reset the target branches")
	if err := fs.Parse(args); err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}

	if cfg.Owner == "" {
		return config{}, errors.New("config: owner is required: set -owner or CIRCLE_PROJECT_USERNAME")
	}
	if cfg.Repo == "" {
		return config{}, errors.New("config: repo is required: set -repo or CIRCLE_PROJECT_REPONAME")
	}

	for _, t := range strings.Split(targets, ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.Targets = append(cfg.Targets, t)
		}
	}
	if len(cfg.Targets) == 0 {
		return config{}, errors.New("config: -targets needs at least one branch")
	}
	for _, t := range cfg.Targets {
		if t == cfg.Base {
			return config{}, fmt.Errorf("config: -base %q must not be one of -targets", cfg.Base)
		}
	}

	re, err := drift.CompilePattern(pattern)
	if err != nil {
		return config{}, fmt.Errorf("config: -ticket-pattern: %w", err)
	}
	cfg.Pattern = re

	if err := githubAuth(&cfg, getenv); err != nil {
		return config{}, err
	}

	cfg.ClickUpToken = getenv("CLICKUP_API_TOKEN")
	cfg.ClickUpTeamID = getenv("CLICKUP_TEAM_ID")
	for _, v := range []struct{ name, val string }{
		{"CLICKUP_API_TOKEN", cfg.ClickUpToken},
		{"CLICKUP_TEAM_ID", cfg.ClickUpTeamID},
	} {
		if v.val == "" {
			return config{}, fmt.Errorf("config: %s is required", v.name)
		}
	}

	cfg.DiscordWebhook = getenv("DISCORD_WEBHOOK_URL")
	if cfg.DiscordWebhook == "" && !cfg.DryRun {
		return config{}, errors.New("config: DISCORD_WEBHOOK_URL is required unless -dry-run is set")
	}

	cfg.Users, err = loadUsers(usersFile, getenv, readFile)
	if err != nil {
		return config{}, err
	}
	return cfg, nil
}

// githubAuth prefers a token; otherwise all three App variables are needed.
func githubAuth(cfg *config, getenv func(string) string) error {
	if cfg.GitHubToken = getenv("GITHUB_TOKEN"); cfg.GitHubToken != "" {
		return nil
	}
	cfg.AppID = getenv("TICKET_DRIFT_APP_ID")
	cfg.AppInstallationID = getenv("TICKET_DRIFT_APP_INSTALLATION_ID")
	key := getenv("TICKET_DRIFT_APP_PRIVATE_KEY")
	if cfg.AppID == "" && cfg.AppInstallationID == "" && key == "" {
		return errors.New("config: GitHub credentials missing: set GITHUB_TOKEN, or TICKET_DRIFT_APP_ID, TICKET_DRIFT_APP_INSTALLATION_ID and TICKET_DRIFT_APP_PRIVATE_KEY")
	}
	for _, v := range []struct{ name, val string }{
		{"TICKET_DRIFT_APP_ID", cfg.AppID},
		{"TICKET_DRIFT_APP_INSTALLATION_ID", cfg.AppInstallationID},
		{"TICKET_DRIFT_APP_PRIVATE_KEY", key},
	} {
		if v.val == "" {
			return fmt.Errorf("config: %s is required when GITHUB_TOKEN is not set", v.name)
		}
	}
	pem, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		// err is deliberately not wrapped: it could echo part of the secret.
		return errors.New("config: TICKET_DRIFT_APP_PRIVATE_KEY is not valid base64")
	}
	cfg.AppKey = pem
	return nil
}

func loadUsers(file string, getenv func(string) string, readFile func(string) ([]byte, error)) (map[string]string, error) {
	var raw []byte
	source := "TICKET_DRIFT_USERS"
	if file != "" {
		b, err := readFile(file)
		if err != nil {
			return nil, fmt.Errorf("config: read -users file: %w", err)
		}
		raw, source = b, "-users file"
	} else {
		raw = []byte(getenv("TICKET_DRIFT_USERS"))
	}

	users := map[string]string{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return users, nil
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("config: %s must be a JSON object of string to string: %w", source, err)
	}
	for k, v := range parsed {
		users[strings.ToLower(k)] = v
	}
	return users, nil
}

// Command ticket-drift reports tickets merged to the base branch that have
// not reached the target branches, and posts the result to Discord.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/discord"
	"github.com/dorim-group/ticket-drift/internal/github"
)

// Set via -ldflags at release time.
var version, commit = "dev", "none"

const usageText = `ticket-drift %s (%s)

usage: ticket-drift <command> [flags]

commands:
  sync     compare the base branch with the target branches and report drift
  version  print version and exit

Run "ticket-drift sync -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usageText, version, commit)
		os.Exit(2)
	}

	switch cmd := os.Args[1]; cmd {
	case "sync":
		cmdSync(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Printf("ticket-drift %s (%s)\n", version, commit)
	case "help", "-h", "--help":
		fmt.Printf(usageText, version, commit)
	default:
		fmt.Fprintf(os.Stderr, "ticket-drift: unknown command %q\n\n", cmd)
		fmt.Fprintf(os.Stderr, usageText, version, commit)
		os.Exit(2)
	}
}

func cmdSync(args []string) {
	cfg, err := parseConfig(args, os.Getenv, os.ReadFile)
	if err != nil {
		var he helpError
		if errors.As(err, &he) {
			fmt.Print(he.Usage)
			os.Exit(0)
		}
		fail(err)
	}

	var gh *github.Client
	if cfg.GitHubToken != "" {
		gh = github.NewTokenClient(cfg.GitHubToken)
	} else if gh, err = github.NewAppClient(cfg.AppID, cfg.AppInstallationID, cfg.AppKey); err != nil {
		fail(err)
	}
	var dc notifier = noNotifier{}
	if !cfg.DryRun {
		dc = discord.New(cfg.DiscordWebhook)
	}

	rep, runErr := runSync(cfg,
		github.RepoSource{C: gh, Owner: cfg.Owner, Repo: cfg.Repo},
		clickup.New(cfg.ClickUpToken, cfg.ClickUpTeamID),
		dc, os.Stdout)
	// The report is written on failure too: it says how far the run got.
	if cfg.OutJSON != "" {
		if err := writeReport(cfg.OutJSON, rep); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		fail(runErr)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ticket-drift: %v\n", err)
	os.Exit(1)
}

// noNotifier stands in for Discord in -dry-run, where no webhook is
// configured. runSync never posts in a dry run; if it ever did, failing is
// safer than pretending a message was delivered.
type noNotifier struct{}

func (noNotifier) Post(string, []string) error {
	return errors.New("sync: posting is disabled in -dry-run")
}

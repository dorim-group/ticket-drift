// Command ticket-drift finds work merged into dev and stage that has not
// reached main, tells its authors in Discord, then resets dev and stage to main.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/discord"
	"github.com/dorim-group/ticket-drift/internal/github"
)

// Set via -ldflags at release time.
var version, commit = "dev", "none"

const usageText = `ticket-drift %s (%s)

usage: ticket-drift <command> [flags]

commands:
  sync     tell authors of work on dev/stage missing from main, then reset dev and stage to main
  version  print version and exit
  help     print this help and exit

Run "ticket-drift sync -h" for the flags of a command.
`

func main() {
	version = resolveVersion(version, debug.ReadBuildInfo)
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

// resolveVersion falls back to the module version Go records for
// "go install …@vX", which sets no -ldflags. A local build records "(devel)".
func resolveVersion(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "dev" {
		return stamped
	}
	if info, ok := read(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return stamped
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
	if err := syncCommand(cfg, os.Stdout); err != nil {
		fail(err)
	}
}

// syncCommand builds the clients, runs the sync and writes the report. The
// returned error fails the run.
func syncCommand(cfg config, out io.Writer) error {
	var gh *github.Client
	var err error
	if cfg.GitHubToken != "" {
		gh = github.NewTokenClient(cfg.GitHubToken)
	} else if gh, err = github.NewAppClient(cfg.AppID, cfg.AppInstallationID, cfg.AppKey); err != nil {
		return withReport(cfg, newReport(cfg), err)
	}
	var dc notifier = noNotifier{}
	if !cfg.DryRun {
		dc = discord.New(cfg.DiscordWebhook)
	}

	rep, runErr := runSync(cfg,
		github.RepoSource{C: gh, Owner: cfg.Owner, Repo: cfg.Repo},
		clickup.New(cfg.ClickUpToken, cfg.ClickUpTeamID),
		dc, out)
	return withReport(cfg, rep, runErr)
}

// withReport writes rep when -out-json is set, on failure too: it says how
// far the run got.
func withReport(cfg config, rep report, runErr error) error {
	if cfg.OutJSON != "" {
		if err := writeReport(cfg.OutJSON, rep); err != nil {
			return errors.Join(runErr, err)
		}
	}
	return runErr
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

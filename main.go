// Command ticket-drift reports tickets merged to the base branch that have
// not reached the target branches, and posts the result to Discord.
package main

import (
	"fmt"
	"os"
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

// cmdSync is a stub until the sync command lands.
func cmdSync(args []string) {
	fmt.Fprintln(os.Stderr, "ticket-drift: sync: not implemented")
	os.Exit(1)
}

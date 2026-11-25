package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dorim-group/ticket-drift/internal/drift"
)

// report is the -out-json document. Every slice is non-nil so it encodes as
// [] and a consumer never has to tell null from empty.
type report struct {
	Version string         `json:"ticket_drift_version"`
	Owner   string         `json:"owner"`
	Repo    string         `json:"repo"`
	Base    string         `json:"base"`
	BaseSHA string         `json:"base_sha"`
	DryRun  bool           `json:"dry_run"`
	NoReset bool           `json:"no_reset"`
	Targets []targetReport `json:"targets"`
	Groups  []groupReport  `json:"groups"`
}

type targetReport struct {
	Name         string         `json:"name"`
	SHA          string         `json:"sha"`
	Exists       bool           `json:"exists"`
	AheadBy      int            `json:"ahead_by"`
	Shipped      []prReport     `json:"shipped"`
	Lost         []prReport     `json:"lost"`
	BackMerges   []string       `json:"back_merges"`
	Carriers     []string       `json:"carriers"`
	Unattributed []commitReport `json:"unattributed"`
	Reset        resetReport    `json:"reset"`
}

type prReport struct {
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	HeadRef   string `json:"head_ref"`
	HeadSHA   string `json:"head_sha"`
	BaseRef   string `json:"base_ref"`
	Author    string `json:"author"`
	MatchedBy string `json:"matched_by,omitempty"` // shipped PRs only
}

type commitReport struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	AuthorLogin string `json:"author_login"`
	AuthorName  string `json:"author_name"`
}

type resetReport struct {
	Outcome string `json:"outcome"` // done, not_needed, skipped, superseded, aborted_moved, failed
	Reason  string `json:"reason"`
}

type groupReport struct {
	Kind         string   `json:"kind"` // ticket, branch, commits
	Key          string   `json:"key"`
	Targets      []string `json:"targets"`
	People       []string `json:"people"`    // PR and commit authors
	Assignees    []string `json:"assignees"` // ClickUp names
	ClickUpFound bool     `json:"clickup_found"`
	ClickUpURL   string   `json:"clickup_url"`
	Message      string   `json:"message"`
	Posted       bool     `json:"posted"`
}

// newReport is the report of a run that has not started: no targets, no groups.
func newReport(cfg config) report {
	return report{
		Version: version, Owner: cfg.Owner, Repo: cfg.Repo, Base: cfg.Base,
		DryRun: cfg.DryRun, NoReset: cfg.NoReset,
		Targets: []targetReport{}, Groups: []groupReport{},
	}
}

func newTargetReport(name string) targetReport {
	return targetReport{
		Name:         name,
		Shipped:      []prReport{},
		Lost:         []prReport{},
		BackMerges:   []string{},
		Carriers:     []string{},
		Unattributed: []commitReport{},
	}
}

func (t *targetReport) fill(res drift.TargetResult) {
	t.AheadBy = res.AheadBy
	for _, s := range res.Shipped {
		p := toPRReport(s.PR)
		p.MatchedBy = string(s.MatchedBy)
		t.Shipped = append(t.Shipped, p)
	}
	for _, p := range res.Lost {
		t.Lost = append(t.Lost, toPRReport(p))
	}
	t.BackMerges = append(t.BackMerges, res.BackMerges...)
	t.Carriers = append(t.Carriers, res.Carriers...)
	for _, c := range res.Unattributed {
		t.Unattributed = append(t.Unattributed, commitReport{
			SHA: c.SHA, Message: c.Message, AuthorLogin: c.AuthorLogin, AuthorName: c.AuthorName,
		})
	}
}

func toPRReport(p drift.PullRequest) prReport {
	return prReport{
		Number: p.Number, URL: p.URL, Title: p.Title,
		HeadRef: p.HeadRef, HeadSHA: p.HeadSHA, BaseRef: p.BaseRef,
		Author: p.AuthorLogin,
	}
}

func newGroupReport(g drift.Group) groupReport {
	kind := map[drift.GroupKind]string{
		drift.KindTicket:  "ticket",
		drift.KindBranch:  "branch",
		drift.KindCommits: "commits",
	}[g.Kind]
	people := append(append([]string{}, g.Logins...), g.Names...)
	return groupReport{Kind: kind, Key: g.Key, Targets: append([]string{}, g.Targets()...), People: people, Assignees: []string{}}
}

func writeReport(path string, r report) error {
	blob, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return nil
}

package drift

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

type GroupKind int

const (
	KindTicket  GroupKind = iota // Key is a ticket ID
	KindBranch                   // Key is a branch name with no ticket ID
	KindCommits                  // Key is the author's login, or git name when there is none
)

type Item struct {
	Target string
	PR     *PullRequest // exactly one of PR, Commit is set
	Commit *Commit
}

type Group struct {
	Kind   GroupKind
	Key    string
	Items  []Item   // in the order targets were given, then PR number / walk order
	Logins []string // distinct GitHub logins, first-seen order; never ""
	Names  []string // git author names of commits that have no login
}

// Targets returns the distinct targets of the items, in item order.
func (g Group) Targets() []string {
	var out []string
	for _, it := range g.Items {
		if !slices.Contains(out, it.Target) {
			out = append(out, it.Target)
		}
	}
	return out
}

type groupID struct {
	kind GroupKind
	key  string
}

// Groups returns ticket groups sorted by Key, then branch groups, then commit groups.
// A PR whose branch names several tickets lands in each of their groups, so
// every ticket's message lists it.
func Groups(results []TargetResult, re *regexp.Regexp) []Group {
	byID := map[groupID]*Group{}
	var order []*Group
	get := func(kind GroupKind, key string) *Group {
		id := groupID{kind, key}
		g := byID[id]
		if g == nil {
			g = &Group{Kind: kind, Key: key}
			byID[id] = g
			order = append(order, g)
		}
		return g
	}
	addLogin := func(g *Group, login string) {
		if login != "" && !slices.Contains(g.Logins, login) {
			g.Logins = append(g.Logins, login)
		}
	}

	for _, r := range results {
		lost := slices.Clone(r.Lost)
		sort.SliceStable(lost, func(i, j int) bool { return lost[i].Number < lost[j].Number })
		for _, p := range lost {
			keys := TicketIDs(re, p.HeadRef)
			kind := KindTicket
			if len(keys) == 0 {
				keys, kind = []string{p.HeadRef}, KindBranch
			}
			for _, k := range keys {
				g := get(kind, k)
				g.Items = append(g.Items, Item{Target: r.Target, PR: &p})
				addLogin(g, p.AuthorLogin)
			}
		}
		for _, c := range r.Unattributed {
			key := c.AuthorLogin
			if key == "" {
				key = c.AuthorName
			}
			g := get(KindCommits, key)
			g.Items = append(g.Items, Item{Target: r.Target, Commit: &c})
			if c.AuthorLogin != "" {
				addLogin(g, c.AuthorLogin)
			} else if !slices.Contains(g.Names, c.AuthorName) {
				g.Names = append(g.Names, c.AuthorName)
			}
		}
	}

	out := make([]Group, 0, len(order))
	for _, g := range order {
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		// Case-insensitive so logins and names read alphabetically.
		a, b := strings.ToLower(out[i].Key), strings.ToLower(out[j].Key)
		if a != b {
			return a < b
		}
		return out[i].Key < out[j].Key
	})
	return out
}

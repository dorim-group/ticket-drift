package drift

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"
)

func pr(n int, head, login string) PullRequest {
	return PullRequest{Number: n, HeadRef: head, AuthorLogin: login}
}

func groupRE(t *testing.T) *regexp.Regexp {
	t.Helper()
	re, err := CompilePattern(`IT-\d+`)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

func groupPRNumbers(g Group) []int {
	var out []int
	for _, it := range g.Items {
		out = append(out, it.PR.Number)
	}
	return out
}

func TestGroupsOnePerTicketAcrossTargets(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(1098, "IT-10249/responsible-managers-dev", "bob")}},
		{Target: "stage", Lost: []PullRequest{pr(1062, "IT-10249/responsible-managers", "bob")}},
	}, groupRE(t))
	if len(gs) != 1 {
		t.Fatalf("groups = %d, want 1", len(gs))
	}
	g := gs[0]
	if g.Kind != KindTicket || g.Key != "IT-10249" {
		t.Errorf("kind/key = %v/%q", g.Kind, g.Key)
	}
	if got := groupPRNumbers(g); !reflect.DeepEqual(got, []int{1098, 1062}) {
		t.Errorf("items = %v", got)
	}
	if g.Items[0].Target != "dev" || g.Items[1].Target != "stage" {
		t.Errorf("item targets = %q, %q", g.Items[0].Target, g.Items[1].Target)
	}
	if !reflect.DeepEqual(g.Logins, []string{"bob"}) {
		t.Errorf("logins = %v", g.Logins)
	}
	if got := g.Targets(); !reflect.DeepEqual(got, []string{"dev", "stage"}) {
		t.Errorf("targets = %v", got)
	}
}

func TestGroupsSeparateTicketsSeparatePeople(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(2, "IT-2/b", "bob"), pr(1, "IT-1/a", "ann")}},
	}, groupRE(t))
	if len(gs) != 2 {
		t.Fatalf("groups = %d, want 2", len(gs))
	}
	if gs[0].Key != "IT-1" || gs[1].Key != "IT-2" {
		t.Errorf("keys = %q, %q", gs[0].Key, gs[1].Key)
	}
	if !reflect.DeepEqual(gs[0].Logins, []string{"ann"}) || !reflect.DeepEqual(gs[1].Logins, []string{"bob"}) {
		t.Errorf("logins = %v, %v", gs[0].Logins, gs[1].Logins)
	}
}

func TestGroupsTwoAuthorsOnOneTicket(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(1, "IT-1/a", "ann"), pr(2, "IT-1/b", "bob")}},
	}, groupRE(t))
	if len(gs) != 1 {
		t.Fatalf("groups = %d, want 1", len(gs))
	}
	if !reflect.DeepEqual(gs[0].Logins, []string{"ann", "bob"}) {
		t.Errorf("logins = %v", gs[0].Logins)
	}
}

func TestGroupsBranchWithTwoTicketsAppearsInBoth(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(5, "qa/IT-9123-IT-8982-in-dev", "ann")}},
	}, groupRE(t))
	if len(gs) != 2 {
		t.Fatalf("groups = %d, want 2", len(gs))
	}
	if gs[0].Key != "IT-8982" || gs[1].Key != "IT-9123" {
		t.Errorf("keys = %q, %q", gs[0].Key, gs[1].Key)
	}
	for _, g := range gs {
		if got := groupPRNumbers(g); !reflect.DeepEqual(got, []int{5}) {
			t.Errorf("%s items = %v", g.Key, got)
		}
	}
}

func TestGroupsBranchWithoutTicket(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(7, "esf-hotfix", "dan"), pr(8, "IT-1/a", "ann")}},
	}, groupRE(t))
	if len(gs) != 2 {
		t.Fatalf("groups = %d, want 2", len(gs))
	}
	if gs[0].Kind != KindTicket {
		t.Errorf("first kind = %v, want ticket", gs[0].Kind)
	}
	g := gs[1]
	if g.Kind != KindBranch || g.Key != "esf-hotfix" || !reflect.DeepEqual(g.Logins, []string{"dan"}) {
		t.Errorf("branch group = %+v", g)
	}
}

func TestGroupsUnattributedByAuthor(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Unattributed: []Commit{
			{SHA: "c1", AuthorLogin: "ann", AuthorName: "Ann"},
			{SHA: "c2", AuthorLogin: "ann", AuthorName: "Ann"},
			{SHA: "c3", AuthorName: "Old Dev"},
		}},
	}, groupRE(t))
	if len(gs) != 2 {
		t.Fatalf("groups = %d, want 2", len(gs))
	}
	a, b := gs[0], gs[1]
	if a.Kind != KindCommits || a.Key != "ann" || len(a.Items) != 2 ||
		!reflect.DeepEqual(a.Logins, []string{"ann"}) || len(a.Names) != 0 {
		t.Errorf("ann group = %+v", a)
	}
	if b.Kind != KindCommits || b.Key != "Old Dev" || len(b.Items) != 1 ||
		len(b.Logins) != 0 || !reflect.DeepEqual(b.Names, []string{"Old Dev"}) {
		t.Errorf("Old Dev group = %+v", b)
	}
	if a.Items[0].Commit == nil || a.Items[0].PR != nil || a.Items[0].Commit.SHA != "c1" {
		t.Errorf("item = %+v", a.Items[0])
	}
}

func TestGroupsDeletedAuthorHasNoLogin(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(8, "IT-3/x", "")}},
	}, groupRE(t))
	if len(gs) != 1 || gs[0].Key != "IT-3" {
		t.Fatalf("groups = %+v", gs)
	}
	if len(gs[0].Logins) != 0 {
		t.Errorf("logins = %q, want none", gs[0].Logins)
	}
}

// A git author name that reads like someone's login is still another person:
// one group must never name both.
func TestGroupsLoginAndNameKeptApart(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Unattributed: []Commit{
			{SHA: "c1", AuthorLogin: "ann", AuthorName: "Ann Lee"},
			{SHA: "c2", AuthorName: "ann"},
		}},
	}, groupRE(t))
	if len(gs) != 2 {
		t.Fatalf("groups = %+v, want 2", gs)
	}
	a, b := gs[0], gs[1]
	if a.Key != "ann" || !reflect.DeepEqual(a.Logins, []string{"ann"}) || len(a.Names) != 0 || len(a.Items) != 1 {
		t.Errorf("login group = %+v", a)
	}
	if b.Key != "ann" || len(b.Logins) != 0 || !reflect.DeepEqual(b.Names, []string{"ann"}) || len(b.Items) != 1 {
		t.Errorf("name group = %+v", b)
	}
}

func TestGroupsPlaceholderKeys(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(4, "", "bob")}, Unattributed: []Commit{{SHA: "c1"}}},
	}, groupRE(t))
	if len(gs) != 2 {
		t.Fatalf("groups = %+v, want 2", gs)
	}
	if g := gs[0]; g.Kind != KindBranch || g.Key != "(no branch)" || !reflect.DeepEqual(g.Logins, []string{"bob"}) {
		t.Errorf("branch group = %+v", g)
	}
	if g := gs[1]; g.Kind != KindCommits || g.Key != "unknown" || len(g.Logins) != 0 || len(g.Names) != 0 {
		t.Errorf("commit group = %+v", g)
	}
}

// Items run in the order the targets were given, then by PR number, however
// the PRs came in.
func TestGroupsItemOrderAcrossTargets(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(5, "IT-1/b", "ann"), pr(3, "IT-1/a", "ann")}},
		{Target: "stage", Lost: []PullRequest{pr(9, "IT-1/c", "ann"), pr(2, "IT-1/d", "ann")}},
	}, groupRE(t))
	if len(gs) != 1 {
		t.Fatalf("groups = %d, want 1", len(gs))
	}
	var got []string
	for _, it := range gs[0].Items {
		got = append(got, fmt.Sprintf("%s#%d", it.Target, it.PR.Number))
	}
	if want := []string{"dev#3", "dev#5", "stage#2", "stage#9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}
}

func TestGroupsKeyOrderCaseInsensitiveThenBytes(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Unattributed: []Commit{
			{SHA: "c1", AuthorLogin: "ann"},
			{SHA: "c2", AuthorLogin: "Bob"},
			{SHA: "c3", AuthorLogin: "Ann"},
		}},
	}, groupRE(t))
	var keys []string
	for _, g := range gs {
		keys = append(keys, g.Key)
	}
	if want := []string{"Ann", "ann", "Bob"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestGroupTargetsDeduplicated(t *testing.T) {
	gs := Groups([]TargetResult{
		{Target: "dev", Lost: []PullRequest{pr(1, "IT-1/a", "ann"), pr(2, "IT-1/b", "ann")}},
		{Target: "stage", Lost: []PullRequest{pr(3, "IT-1/c", "ann")}},
	}, groupRE(t))
	if len(gs) != 1 || len(gs[0].Items) != 3 {
		t.Fatalf("groups = %+v, want one with 3 items", gs)
	}
	if got := gs[0].Targets(); !reflect.DeepEqual(got, []string{"dev", "stage"}) {
		t.Errorf("targets = %v, want [dev stage]", got)
	}
}

func TestGroupsEmpty(t *testing.T) {
	if gs := Groups([]TargetResult{{Target: "dev"}}, groupRE(t)); len(gs) != 0 {
		t.Errorf("groups = %d, want 0", len(gs))
	}
}

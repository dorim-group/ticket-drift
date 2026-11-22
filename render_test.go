package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/drift"
)

func pr(target string, n int, ref string) drift.Item {
	return drift.Item{Target: target, PR: &drift.PullRequest{
		Number:  n,
		URL:     fmt.Sprintf("https://github.com/o/r/pull/%d", n),
		HeadRef: ref,
	}}
}

func baseInput(g drift.Group) renderInput {
	return renderInput{Owner: "o", Repo: "r", Base: "main", Group: g, Users: map[string]string{}, WillReset: true}
}

func lines(s string) []string { return strings.Split(s, "\n") }

func TestRenderTicketTwoTargets(t *testing.T) {
	want := "**IT-10249** · Responsible managers\n" +
		"https://app.clickup.com/t/86c0 · статус в ClickUp: in stage\n" +
		"`service-contractors`: **dev** и **stage** выровнены до main. Тикет нужно задеплоить заново.\n" +
		"dev — [#1098](https://github.com/dorim-group/service-contractors/pull/1098) `IT-10249/responsible-managers-dev`\n" +
		"stage — [#1062](https://github.com/dorim-group/service-contractors/pull/1062) `IT-10249/responsible-managers`\n" +
		"<@111>"
	in := renderInput{
		Owner: "dorim-group", Repo: "service-contractors", Base: "main",
		Group: drift.Group{
			Kind: drift.KindTicket, Key: "IT-10249", Logins: []string{"Bob"},
			Items: []drift.Item{
				{Target: "dev", PR: &drift.PullRequest{Number: 1098, URL: "https://github.com/dorim-group/service-contractors/pull/1098", HeadRef: "IT-10249/responsible-managers-dev"}},
				{Target: "stage", PR: &drift.PullRequest{Number: 1062, URL: "https://github.com/dorim-group/service-contractors/pull/1062", HeadRef: "IT-10249/responsible-managers"}},
			},
		},
		Task:      &clickup.Task{Name: "Responsible managers", URL: "https://app.clickup.com/t/86c0", Status: "in stage"},
		Users:     map[string]string{"bob": "111"},
		WillReset: true,
	}
	got, ids := renderGroup(in)
	if got != want {
		t.Errorf("content:\n got %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(ids, []string{"111"}) {
		t.Errorf("mentionIDs = %v", ids)
	}
}

func ticketGroup(logins ...string) drift.Group {
	return drift.Group{Kind: drift.KindTicket, Key: "IT-5", Logins: logins,
		Items: []drift.Item{pr("dev", 1, "IT-5/x")}}
}

func TestRenderTicketOneTarget(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.Task = &clickup.Task{Name: "N", URL: "u", Status: "s"}
	got, _ := renderGroup(in)
	if l := lines(got)[2]; l != "`r`: **dev** выровнен до main. Тикет нужно задеплоить заново." {
		t.Errorf("line 3 = %q", l)
	}
}

func TestRenderTicketMissingInClickUp(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.TaskMissing = true
	l := lines(func() string { s, _ := renderGroup(in); return s }())
	if l[0] != "**IT-5**" || l[1] != "IT-5 не найден в ClickUp" {
		t.Errorf("lines = %q", l[:2])
	}
}

func TestRenderBranchGroup(t *testing.T) {
	g := drift.Group{Kind: drift.KindBranch, Key: "esf-hotfix", Logins: []string{"bob"},
		Items: []drift.Item{pr("dev", 7, "esf-hotfix")}}
	got, _ := renderGroup(baseInput(g))
	want := "**Ветка без тикета** · `esf-hotfix`\n" +
		"`r`: **dev** выровнен до main. Изменения нужно задеплоить заново.\n" +
		"dev — [#7](https://github.com/o/r/pull/7) `esf-hotfix`\n" +
		"@bob"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRenderCommitsGroup(t *testing.T) {
	g := drift.Group{Kind: drift.KindCommits, Key: "ann", Logins: []string{"ann"},
		Items: []drift.Item{{Target: "dev", Commit: &drift.Commit{SHA: "abc1234def5678", Message: "fix typo\n\nlong body"}}}}
	got, _ := renderGroup(baseInput(g))
	want := "**Коммиты без PR** · ann\n" +
		"`r`: **dev** выровнен до main. Изменения нужно задеплоить заново.\n" +
		"dev — [`abc1234`](https://github.com/o/r/commit/abc1234def5678) fix typo\n" +
		"@ann"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRenderCommitMessageCut(t *testing.T) {
	g := drift.Group{Kind: drift.KindCommits, Key: "ann", Logins: []string{"ann"},
		Items: []drift.Item{{Target: "dev", Commit: &drift.Commit{SHA: "abc1234def", Message: strings.Repeat("я", 100)}}}}
	got, _ := renderGroup(baseInput(g))
	if l := lines(got)[2]; !strings.HasSuffix(l, ") "+strings.Repeat("я", 80)) {
		t.Errorf("item line = %q", l)
	}
}

func lastLine(in renderInput) (string, []string) {
	got, ids := renderGroup(in)
	l := lines(got)
	return l[len(l)-1], ids
}

func TestRenderUnmappedLoginIsPlainText(t *testing.T) {
	l, ids := lastLine(baseInput(ticketGroup("carol")))
	if l != "@carol" || len(ids) != 0 {
		t.Errorf("last = %q ids = %v", l, ids)
	}
}

func TestRenderTwoAuthors(t *testing.T) {
	in := baseInput(ticketGroup("bob", "carol"))
	in.Users = map[string]string{"bob": "111"}
	l, ids := lastLine(in)
	if l != "<@111> @carol" || !reflect.DeepEqual(ids, []string{"111"}) {
		t.Errorf("last = %q ids = %v", l, ids)
	}
}

func TestRenderDuplicateIDsDistinct(t *testing.T) {
	in := baseInput(ticketGroup("bob", "Bobby"))
	in.Users = map[string]string{"bob": "111", "bobby": "111"}
	_, ids := lastLine(in)
	if !reflect.DeepEqual(ids, []string{"111"}) {
		t.Errorf("ids = %v", ids)
	}
}

func TestRenderDeletedAuthor(t *testing.T) {
	l, _ := lastLine(baseInput(ticketGroup()))
	if l != "@ghost" {
		t.Errorf("last = %q", l)
	}
}

func TestRenderCommitAuthorWithoutLogin(t *testing.T) {
	g := drift.Group{Kind: drift.KindCommits, Key: "Old Dev", Names: []string{"Old Dev"},
		Items: []drift.Item{{Target: "dev", Commit: &drift.Commit{SHA: "abc1234def", Message: "m"}}}}
	l, _ := lastLine(baseInput(g))
	if l != "Old Dev" {
		t.Errorf("last = %q", l)
	}
}

func TestRenderNoReset(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.WillReset = false
	got, _ := renderGroup(in)
	if l := lines(got)[0]; l != "**IT-5**" {
		t.Errorf("line 1 = %q", l)
	}
	if l := lines(got)[1]; l != "`r`: **dev** будет выровнен до main при следующем сбросе. Тикет пропадёт с этого стенда." {
		t.Errorf("line 2 = %q", l)
	}
	in.Group.Items = append(in.Group.Items, pr("qa", 2, "IT-5/y"))
	got, _ = renderGroup(in)
	if l := lines(got)[1]; l != "`r`: **dev** и **qa** будут выровнены до main при следующем сбросе. Тикет пропадёт с этих стендов." {
		t.Errorf("several = %q", l)
	}
	in.Group = drift.Group{Kind: drift.KindBranch, Key: "b", Logins: []string{"bob"}, Items: []drift.Item{pr("dev", 3, "b")}}
	got, _ = renderGroup(in)
	if l := lines(got)[1]; l != "`r`: **dev** будет выровнен до main при следующем сбросе. Изменения пропадут с этого стенда." {
		t.Errorf("changes = %q", l)
	}
}

func TestRenderThreeTargetsJoin(t *testing.T) {
	g := ticketGroup("bob")
	g.Items = []drift.Item{pr("dev", 1, "a"), pr("qa", 2, "a"), pr("stage", 3, "a")}
	got, _ := renderGroup(baseInput(g))
	if !strings.Contains(lines(got)[1], "**dev**, **qa** и **stage** выровнены") {
		t.Errorf("got %q", lines(got)[1])
	}
}

func TestRenderLongTitleTruncated(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.Task = &clickup.Task{Name: strings.Repeat("ж", 500), URL: "u", Status: "s"}
	got, _ := renderGroup(in)
	want := "**IT-5** · " + strings.Repeat("ж", 200) + "…"
	if l := lines(got)[0]; l != want {
		t.Errorf("line 1 = %q", l)
	}
}

func TestRenderManyItemsFitsLimit(t *testing.T) {
	g := ticketGroup("bob")
	g.Items = nil
	for i := 0; i < 60; i++ {
		g.Items = append(g.Items, pr("dev", 1000+i, "IT-5/"+strings.Repeat("long-branch-name", 3)))
	}
	in := baseInput(g)
	got, _ := renderGroup(in)
	if n := utf8.RuneCountInString(got); n > maxMessageRunes {
		t.Errorf("%d runes", n)
	}
	l := lines(got)
	if !strings.HasPrefix(l[len(l)-2], "…и ещё ") {
		t.Errorf("penultimate = %q", l[len(l)-2])
	}
	if l[len(l)-1] != "@bob" {
		t.Errorf("last = %q", l[len(l)-1])
	}
	var shown int
	for _, x := range l {
		if strings.HasPrefix(x, "dev — ") {
			shown++
		}
	}
	if want := fmt.Sprintf("…и ещё %d", 60-shown); l[len(l)-2] != want {
		t.Errorf("marker = %q, want %q", l[len(l)-2], want)
	}
}

func TestRenderCorrectionAndSummary(t *testing.T) {
	if got, want := renderCorrection("service-contractors", "dev", "main", "конфликт защиты ветки"),
		"`service-contractors`: ветка **dev** НЕ выровнена до main — конфликт защиты ветки. Сообщения выше о сбросе **dev** неактуальны."; got != want {
		t.Errorf("correction = %q", got)
	}
	if got, want := renderSummary("service-contractors", []string{"dev", "stage"}, "main", "abc1234", 6),
		"`service-contractors`: **dev** и **stage** выровнены до main (`abc1234`). Тикетов для повторного деплоя: 6."; got != want {
		t.Errorf("summary = %q", got)
	}
	if got, want := renderSummary("r", []string{"dev"}, "main", "abc1234", 1),
		"`r`: **dev** выровнен до main (`abc1234`). Тикетов для повторного деплоя: 1."; got != want {
		t.Errorf("summary one = %q", got)
	}
}

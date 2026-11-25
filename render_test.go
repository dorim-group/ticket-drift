package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
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
	if n := utf8.RuneCountInString(got); n > maxMessageUnits {
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

func TestRenderManyMentionsFitsLimit(t *testing.T) {
	g := ticketGroup()
	in := baseInput(g)
	for i := 0; i < 400; i++ {
		login := fmt.Sprintf("user%d", i)
		in.Group.Logins = append(in.Group.Logins, login)
		in.Users[login] = fmt.Sprintf("%018d", 100000000000000000+i)
	}
	got, ids := renderGroup(in)
	if n := utf8.RuneCountInString(got); n > maxMessageUnits {
		t.Errorf("%d runes", n)
	}
	l := lines(got)
	last := l[len(l)-1]
	if !strings.HasPrefix(last, "<@") || !strings.HasSuffix(last, "…") {
		t.Errorf("last = %q", last)
	}
	if len(ids) == 0 || len(ids) >= 400 {
		t.Errorf("len(ids) = %d", len(ids))
	}
	for _, id := range ids {
		if !strings.Contains(got, "<@"+id+">") {
			t.Errorf("id %s not in content", id)
		}
	}
}

func TestRenderLongStatusAndKeyCapped(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.Task = &clickup.Task{Name: "N", URL: "https://x/t/1", Status: strings.Repeat("с", 3000)}
	got, _ := renderGroup(in)
	if n := utf8.RuneCountInString(got); n > maxMessageUnits {
		t.Errorf("%d runes", n)
	}
	if want := "https://x/t/1 · статус в ClickUp: " + strings.Repeat("с", 100) + "…"; lines(got)[1] != want {
		t.Errorf("line 2 = %q", lines(got)[1])
	}
	g := drift.Group{Kind: drift.KindBranch, Key: strings.Repeat("k", 3000), Logins: []string{"bob"},
		Items: []drift.Item{pr("dev", 1, "b")}}
	got, _ = renderGroup(baseInput(g))
	if n := utf8.RuneCountInString(got); n > maxMessageUnits {
		t.Errorf("%d runes", n)
	}
}

func TestRenderOverlongURLOmitted(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.Task = &clickup.Task{Name: "N", URL: "https://x/" + strings.Repeat("u", 390), Status: "in stage"}
	got, _ := renderGroup(in)
	if l := lines(got)[1]; l != "статус в ClickUp: in stage" {
		t.Errorf("line 2 = %q", l)
	}
}

func TestRenderCaseOnlyDuplicateLoginShownOnce(t *testing.T) {
	l, _ := lastLine(baseInput(ticketGroup("Carol", "carol")))
	if l != "@Carol" {
		t.Errorf("last = %q", l)
	}
}

// discordUnits counts s as Discord does, in UTF-16 code units.
func discordUnits(s string) int { return len(utf16.Encode([]rune(s))) }

func TestRenderResetLineCapped(t *testing.T) {
	g := ticketGroup("bob")
	g.Items = nil
	for _, name := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"} {
		g.Items = append(g.Items, pr(name, 1, "IT-5/x"))
	}
	in := baseInput(g)
	in.Repo, in.Base = strings.Repeat("r", 3000), strings.Repeat("b", 3000)
	got, _ := renderGroup(in)
	if n := discordUnits(got); n > maxMessageUnits {
		t.Errorf("%d UTF-16 units, want at most %d", n, maxMessageUnits)
	}
	want := "`" + strings.Repeat("r", 100) + "…`: **t1**, **t2**, **t3**, **t4**, **t5**, … выровнены до " +
		strings.Repeat("b", 100) + "…. Тикет нужно задеплоить заново."
	if l := lines(got)[1]; l != want {
		t.Errorf("reset line = %q\nwant %q", l, want)
	}
}

func TestRenderResetLineCapsTargetNames(t *testing.T) {
	g := ticketGroup("bob")
	g.Items = []drift.Item{pr(strings.Repeat("d", 3000), 1, "IT-5/x")}
	got, _ := renderGroup(baseInput(g))
	if want := "`r`: **" + strings.Repeat("d", 100) + "…** выровнен до main. Тикет нужно задеплоить заново."; lines(got)[1] != want {
		t.Errorf("reset line = %q", lines(got)[1])
	}
}

// A group with nothing to list never claims it dropped something.
func TestRenderNoDroppedMarkerWithoutItems(t *testing.T) {
	in := baseInput(drift.Group{Kind: drift.KindCommits, Key: "ann"})
	for i := 0; i < 400; i++ {
		login := fmt.Sprintf("user%d", i)
		in.Group.Logins = append(in.Group.Logins, login)
		in.Users[login] = fmt.Sprintf("%018d", 100000000000000000+i)
	}
	got, _ := renderGroup(in)
	if strings.Contains(got, "…и ещё") {
		t.Errorf("content has a dropped-items marker:\n%s", got)
	}
	if n := discordUnits(got); n > maxMessageUnits {
		t.Errorf("%d UTF-16 units", n)
	}
}

func TestRenderSkipsEmptyItem(t *testing.T) {
	g := ticketGroup("bob")
	g.Items = []drift.Item{{Target: "dev"}, pr("dev", 1, "IT-5/x")}
	got, _ := renderGroup(baseInput(g))
	want := "**IT-5**\n" +
		"`r`: **dev** выровнен до main. Тикет нужно задеплоить заново.\n" +
		"dev — [#1](https://github.com/o/r/pull/1) `IT-5/x`\n" +
		"@bob"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRenderNameWithLineBreaks(t *testing.T) {
	g := drift.Group{Kind: drift.KindCommits, Key: "Old\r\nDev", Names: []string{"Old\r\nDev", "New\nDev"},
		Items: []drift.Item{{Target: "dev", Commit: &drift.Commit{SHA: "abc1234def", Message: "m"}}}}
	got, _ := renderGroup(baseInput(g))
	l := lines(got)
	if len(l) != 4 || l[0] != "**Коммиты без PR** · Old Dev" || l[3] != "Old Dev New Dev" {
		t.Errorf("lines = %q", l)
	}
	if strings.Contains(got, "\r") {
		t.Errorf("content keeps a CR: %q", got)
	}
}

// Discord counts UTF-16 code units: 200 emoji are 400 of them, which a rune
// count would see as 200.
func TestRenderAstralTitleFitsLimit(t *testing.T) {
	in := baseInput(ticketGroup("bob"))
	in.Task = &clickup.Task{Name: strings.Repeat("😀", 200), URL: "u", Status: "s"}
	in.Group.Items = nil
	for i := 0; i < 60; i++ {
		in.Group.Items = append(in.Group.Items, pr("dev", 1000+i, "IT-5/"+strings.Repeat("long-branch-name", 3)))
	}
	got, _ := renderGroup(in)
	if n := discordUnits(got); n > maxMessageUnits {
		t.Errorf("%d UTF-16 units, want at most %d", n, maxMessageUnits)
	}
	if want := "**IT-5** · " + strings.Repeat("😀", 200); lines(got)[0] != want {
		t.Errorf("title line cut: %q", lines(got)[0])
	}
}

func TestRenderCommitMessageTrailingCR(t *testing.T) {
	g := drift.Group{Kind: drift.KindCommits, Key: "ann", Logins: []string{"ann"},
		Items: []drift.Item{{Target: "dev", Commit: &drift.Commit{SHA: "abc1234def", Message: "fix\r\nbody"}}}}
	got, _ := renderGroup(baseInput(g))
	if strings.Contains(got, "\r") {
		t.Errorf("got %q", got)
	}
}

func TestRenderAssignees(t *testing.T) {
	ann := clickup.Assignee{Name: "Ann Lee", Email: "Ann@x.io"}
	bob := clickup.Assignee{Name: "Bob Ray", Email: "bob@x.io"}
	ivan := clickup.Assignee{Name: "Ivan\nPetrov", Email: "ivan@x.io"}
	for name, tc := range map[string]struct {
		assignees []clickup.Assignee
		logins    []string
		want      string
		ids       []string
	}{
		"all mapped":                 {[]clickup.Assignee{ann, bob}, []string{"opener"}, "<@111> <@222>", []string{"111", "222"}},
		"one mapped":                 {[]clickup.Assignee{ann, ivan}, []string{"opener"}, "<@111> Ivan Petrov", []string{"111"}},
		"none mapped":                {[]clickup.Assignee{ivan}, []string{"opener"}, "Ivan Petrov <@999>", []string{"999"}},
		"none mapped, opener absent": {[]clickup.Assignee{ivan}, nil, "Ivan Petrov", nil},
		"no assignee":                {nil, []string{"opener"}, "<@999>", []string{"999"}},
		"no assignee, no opener":     {nil, nil, "@ghost", nil},
		"nameless and unmapped":      {[]clickup.Assignee{{Email: "new@x.io"}}, []string{"opener"}, "<@999>", []string{"999"}},
		"no email":                   {[]clickup.Assignee{{Name: "Ann Lee"}}, []string{"opener"}, "Ann Lee <@999>", []string{"999"}},
		"same person twice":          {[]clickup.Assignee{ann, ann}, nil, "<@111>", []string{"111"}},
	} {
		t.Run(name, func(t *testing.T) {
			in := baseInput(ticketGroup(tc.logins...))
			in.Task = &clickup.Task{Name: "N", URL: "u", Status: "s", Assignees: tc.assignees}
			in.Users = map[string]string{"opener": "999"}
			in.ClickUpUsers = map[string]string{"ann@x.io": "111", "bob@x.io": "222"}
			got, ids := renderGroup(in)
			l := lines(got)
			if l[len(l)-1] != tc.want || !reflect.DeepEqual(ids, tc.ids) {
				t.Errorf("last = %q ids = %v, want %q %v", l[len(l)-1], ids, tc.want, tc.ids)
			}
			if strings.Contains(got, "x.io") {
				t.Errorf("message shows an email: %q", got)
			}
		})
	}
}

func TestRenderManyLongAssigneeNamesFitLimit(t *testing.T) {
	in := baseInput(ticketGroup("opener"))
	in.Task = &clickup.Task{Name: "N", URL: "u", Status: "s"}
	for range 50 {
		in.Task.Assignees = append(in.Task.Assignees, clickup.Assignee{Name: strings.Repeat("😀", 500)})
	}
	got, _ := renderGroup(in)
	if n := discordUnits(got); n > maxMessageUnits {
		t.Errorf("length = %d units, want at most %d", n, maxMessageUnits)
	}
}

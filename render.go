package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/drift"
)

// Discord rejects content over 2000 characters, which it counts in UTF-16
// code units; stay below it.
const maxMessageUnits = 1900

const (
	maxTitleRunes     = 200
	maxKeyRunes       = 100
	maxStatusRunes    = 100
	maxURLRunes       = 300
	maxCommitMsgRunes = 80
	maxNameRunes      = 100 // repo, base and each target name in the reset line; an assignee
	maxResetTargets   = 5
	shortSHALength    = 7
	ghostMention      = "@ghost"
	droppedItemsFmt   = "…и ещё %d"
)

type renderInput struct {
	Owner, Repo  string
	Base         string // "main"
	Group        drift.Group
	Task         *clickup.Task     // nil when there is no ticket or ClickUp has none
	TaskMissing  bool              // a ticket ID that ClickUp does not know
	Users        map[string]string // lower-cased GitHub login -> Discord user ID
	ClickUpUsers map[string]string // lower-cased ClickUp email -> Discord user ID
	WillReset    bool              // false in -no-reset mode
}

func renderGroup(in renderInput) (content string, mentionIDs []string) {
	in.Group.Key = cutRunes(oneLine(in.Group.Key), maxKeyRunes, "…")
	head := headerLines(in)
	var items []string
	for _, it := range in.Group.Items {
		if it.PR != nil || it.Commit != nil { // an empty item has nothing to show
			items = append(items, itemLine(in, it))
		}
	}
	mentions := mentionParts(in)

	build := func(shown []string, m []mention, ellipsis bool) string {
		parts := append(slices.Clone(head), shown...)
		return strings.Join(append(parts, mentionText(m, ellipsis)), "\n")
	}
	fits := func(s string) bool { return discordLen(s) <= maxMessageUnits }

	content = build(items, mentions, false)
	if !fits(content) {
		for keep := len(items) - 1; keep >= 0; keep-- {
			shown := append(slices.Clone(items[:keep]), fmt.Sprintf(droppedItemsFmt, len(items)-keep))
			if content = build(shown, mentions, false); fits(content) {
				return content, idsOf(mentions)
			}
		}
		// Every item is gone and the message is still too long: the mentions
		// line is the only unbounded part left, so shorten it.
		var shown []string
		if len(items) > 0 {
			shown = []string{fmt.Sprintf(droppedItemsFmt, len(items))}
		}
		for left := mentions; len(left) > 0; {
			left = dropMention(left)
			if content = build(shown, left, true); fits(content) {
				return content, idsOf(left)
			}
		}
	}
	return content, idsOf(mentions)
}

func headerLines(in renderInput) []string {
	g := in.Group
	var out []string
	switch g.Kind {
	case drift.KindTicket:
		switch {
		case in.Task != nil:
			status := "статус в ClickUp: " + cutRunes(in.Task.Status, maxStatusRunes, "…")
			// A cut link is useless, so an overlong one is left out.
			if utf8.RuneCountInString(in.Task.URL) <= maxURLRunes {
				status = in.Task.URL + " · " + status
			}
			out = append(out,
				fmt.Sprintf("**%s** · %s", g.Key, cutRunes(in.Task.Name, maxTitleRunes, "…")),
				status)
		case in.TaskMissing:
			out = append(out, fmt.Sprintf("**%s**", g.Key), g.Key+" не найден в ClickUp")
		default:
			out = append(out, fmt.Sprintf("**%s**", g.Key))
		}
	case drift.KindBranch:
		out = append(out, fmt.Sprintf("**Ветка без тикета** · `%s`", g.Key))
	case drift.KindCommits:
		out = append(out, fmt.Sprintf("**Коммиты без PR** · %s", g.Key))
	}
	return append(out, resetLine(in))
}

func resetLine(in renderInput) string {
	targets := in.Group.Targets()
	many := len(targets) > 1
	ticket := in.Group.Kind == drift.KindTicket
	repo, base := cutRunes(in.Repo, maxNameRunes, "…"), cutRunes(in.Base, maxNameRunes, "…")
	pick := func(one, several string) string {
		if many {
			return several
		}
		return one
	}
	if in.WillReset {
		what := "Изменения"
		if ticket {
			what = "Тикет"
		}
		return fmt.Sprintf("`%s`: %s %s до %s. %s нужно задеплоить заново.",
			repo, targetList(targets), pick("выровнен", "выровнены"), base, what)
	}
	var fate string
	switch {
	case ticket:
		fate = "Тикет пропадёт с этого стенда."
		if many {
			fate = "Тикет пропадёт с этих стендов."
		}
	default:
		fate = "Изменения пропадут с этого стенда."
		if many {
			fate = "Изменения пропадут с этих стендов."
		}
	}
	return fmt.Sprintf("`%s`: %s %s до %s при следующем сбросе. %s",
		repo, targetList(targets), pick("будет выровнен", "будут выровнены"), base, fate)
}

// targetList bolds the first maxResetTargets target names, each capped, and
// marks any further ones with "…".
func targetList(targets []string) string {
	shown := make([]string, 0, min(len(targets), maxResetTargets))
	for _, t := range targets[:min(len(targets), maxResetTargets)] {
		shown = append(shown, cutRunes(t, maxNameRunes, "…"))
	}
	if len(targets) <= maxResetTargets {
		return boldJoin(shown)
	}
	for i, t := range shown {
		shown[i] = "**" + t + "**"
	}
	return strings.Join(shown, ", ") + ", …"
}

func itemLine(in renderInput, it drift.Item) string {
	if it.PR != nil {
		return fmt.Sprintf("%s — [#%d](%s) `%s`", it.Target, it.PR.Number, it.PR.URL, it.PR.HeadRef)
	}
	c := it.Commit
	msg, _, _ := strings.Cut(c.Message, "\n")
	msg = strings.TrimSuffix(msg, "\r")
	return fmt.Sprintf("%s — [`%s`](https://github.com/%s/%s/commit/%s) %s",
		it.Target, shortSHA(c.SHA), in.Owner, in.Repo, c.SHA, cutRunes(msg, maxCommitMsgRunes, ""))
}

// mention is one entry of the last line; id is set only for <@id> mentions.
type mention struct{ text, id string }

// mentionParts tags the ticket's ClickUp assignees. The authors are tagged when
// none of them can be, so that somebody is always told.
func mentionParts(in renderInput) []mention {
	out, tagged := assigneeMentions(in)
	if !tagged {
		out = append(out, authorMentions(in)...)
	}
	if len(out) == 0 {
		out = []mention{{text: ghostMention}}
	}
	return out
}

func assigneeMentions(in renderInput) (out []mention, tagged bool) {
	if in.Task == nil {
		return nil, false
	}
	for _, a := range in.Task.Assignees {
		id, ok := in.ClickUpUsers[strings.ToLower(strings.TrimSpace(a.Email))]
		name := strings.TrimSpace(oneLine(a.Name))
		var m mention
		switch {
		case ok:
			m, tagged = mention{"<@" + id + ">", id}, true
		case name != "":
			m = mention{text: cutRunes(name, maxNameRunes, "…")}
		default:
			continue // only an email is left, which is not for the channel
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out, tagged
}

func authorMentions(in renderInput) []mention {
	var out []mention
	var seen []string
	for _, login := range in.Group.Logins {
		lower := strings.ToLower(login)
		if slices.Contains(seen, lower) {
			continue
		}
		seen = append(seen, lower)
		m := mention{text: "@" + login}
		if id, ok := in.Users[lower]; ok {
			m = mention{"<@" + id + ">", id}
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	for _, n := range in.Group.Names {
		out = append(out, mention{text: oneLine(n)})
	}
	return out
}

// dropMention drops the last entry that pings nobody, or the last one when
// every entry pings.
func dropMention(m []mention) []mention {
	i := len(m) - 1
	for j := i; j >= 0; j-- {
		if m[j].id == "" {
			i = j
			break
		}
	}
	return slices.Delete(slices.Clone(m), i, i+1)
}

func mentionText(m []mention, ellipsis bool) string {
	parts := make([]string, 0, len(m)+1)
	for _, x := range m {
		parts = append(parts, x.text)
	}
	if ellipsis {
		parts = append(parts, "…")
	}
	return strings.Join(parts, " ")
}

func idsOf(m []mention) []string {
	var ids []string
	for _, x := range m {
		if x.id != "" && !slices.Contains(ids, x.id) {
			ids = append(ids, x.id)
		}
	}
	return ids
}

func renderCorrection(repo, target, base, reason string) string {
	return fmt.Sprintf("`%s`: ветка **%s** НЕ выровнена до %s — %s. Сообщения выше о сбросе **%s** неактуальны.",
		repo, target, base, reason, target)
}

func renderSummary(repo string, resetTargets []string, base, baseSHA string, groups int) string {
	verb := "выровнен"
	if len(resetTargets) > 1 {
		verb = "выровнены"
	}
	return fmt.Sprintf("`%s`: %s %s до %s (`%s`). Тикетов для повторного деплоя: %d.",
		repo, boldJoin(resetTargets), verb, base, shortSHA(baseSHA), groups)
}

// boldJoin renders "**a**", "**a** и **b**" or "**a**, **b** и **c**".
func boldJoin(targets []string) string {
	b := make([]string, len(targets))
	for i, t := range targets {
		b[i] = "**" + t + "**"
	}
	if len(b) < 2 {
		return strings.Join(b, "")
	}
	return strings.Join(b[:len(b)-1], ", ") + " и " + b[len(b)-1]
}

func shortSHA(sha string) string {
	if len(sha) > shortSHALength {
		return sha[:shortSHALength]
	}
	return sha
}

// oneLine turns each line break in a git author name into a space, so the
// name cannot split a line and the mentions stay on the last one.
var oneLine = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace

// discordLen counts s as Discord does, in UTF-16 code units: a character
// outside the Basic Multilingual Plane, such as most emoji, counts as two.
func discordLen(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// cutRunes cuts s to max runes and appends suffix only when it cut.
func cutRunes(s string, max int, suffix string) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + suffix
}

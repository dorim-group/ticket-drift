package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/drift"
)

// Discord rejects content over 2000 characters; stay below it.
const maxMessageRunes = 1900

const (
	maxTitleRunes     = 200
	maxCommitMsgRunes = 80
	shortSHALength    = 7
	ghostMention      = "@ghost"
	droppedItemsFmt   = "…и ещё %d"
)

type renderInput struct {
	Owner, Repo string
	Base        string // "main"
	Group       drift.Group
	Task        *clickup.Task     // nil when there is no ticket or ClickUp has none
	TaskMissing bool              // a ticket ID that ClickUp does not know
	Users       map[string]string // lower-cased GitHub login -> Discord user ID
	WillReset   bool              // false in -no-reset mode
}

func renderGroup(in renderInput) (content string, mentionIDs []string) {
	g := in.Group
	head := headerLines(in)
	items := make([]string, len(g.Items))
	for i, it := range g.Items {
		items[i] = itemLine(in, it)
	}
	mentions, ids := mentionLine(in)

	build := func(shown []string) string {
		parts := append(slices.Clone(head), shown...)
		return strings.Join(append(parts, mentions), "\n")
	}
	content = build(items)
	if utf8.RuneCountInString(content) > maxMessageRunes {
		for keep := len(items) - 1; keep >= 0; keep-- {
			shown := append(slices.Clone(items[:keep]), fmt.Sprintf(droppedItemsFmt, len(items)-keep))
			content = build(shown)
			if utf8.RuneCountInString(content) <= maxMessageRunes {
				break
			}
		}
	}
	return content, ids
}

func headerLines(in renderInput) []string {
	g := in.Group
	var out []string
	switch g.Kind {
	case drift.KindTicket:
		switch {
		case in.Task != nil:
			out = append(out,
				fmt.Sprintf("**%s** · %s", g.Key, cutRunes(in.Task.Name, maxTitleRunes, "…")),
				fmt.Sprintf("%s · статус в ClickUp: %s", in.Task.URL, in.Task.Status))
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
			in.Repo, boldJoin(targets), pick("выровнен", "выровнены"), in.Base, what)
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
		in.Repo, boldJoin(targets), pick("будет выровнен", "будут выровнены"), in.Base, fate)
}

func itemLine(in renderInput, it drift.Item) string {
	if it.PR != nil {
		return fmt.Sprintf("%s — [#%d](%s) `%s`", it.Target, it.PR.Number, it.PR.URL, it.PR.HeadRef)
	}
	c := it.Commit
	msg, _, _ := strings.Cut(c.Message, "\n")
	return fmt.Sprintf("%s — [`%s`](https://github.com/%s/%s/commit/%s) %s",
		it.Target, shortSHA(c.SHA), in.Owner, in.Repo, c.SHA, cutRunes(msg, maxCommitMsgRunes, ""))
}

func mentionLine(in renderInput) (string, []string) {
	var parts, ids []string
	for _, login := range in.Group.Logins {
		if id, ok := in.Users[strings.ToLower(login)]; ok {
			parts = append(parts, "<@"+id+">")
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		} else {
			parts = append(parts, "@"+login)
		}
	}
	parts = append(parts, in.Group.Names...)
	if len(parts) == 0 {
		parts = []string{ghostMention}
	}
	return strings.Join(parts, " "), ids
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

// cutRunes cuts s to max runes and appends suffix only when it cut.
func cutRunes(s string, max int, suffix string) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + suffix
}

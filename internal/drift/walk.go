package drift

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
)

type walkResult struct {
	PRs          []PullRequest // merged PRs found on the target, each once, in walk order
	BackMerges   []string      // SHAs
	Carriers     []string      // SHAs
	Unattributed []Commit
}

var (
	mergePRRe     = regexp.MustCompile(`^Merge pull request #(\d+) `)
	squashPRRe    = regexp.MustCompile(` \(#(\d+)\)$`)
	mergeBranchRe = regexp.MustCompile(`^Merge (?:remote-tracking )?branch '(?:origin/)?([^']+)'`)
)

// indexPages bounds how far back the closed-PR index reaches; older merges
// are reported as unattributed rather than costing unbounded API calls.
const indexPages = 3

type walkFrame struct {
	start string
	chain string
}

// walk follows the first-parent chain from tip and attributes every commit in
// ahead to a pull request. A merge of another long-lived branch is descended
// into, since the PRs that landed there are also on the target.
func (d *Detector) walk(target, tip string, ahead map[string]Commit) (walkResult, error) {
	var res walkResult
	visited := map[string]bool{}
	seenPR := map[int]bool{}
	indexes := map[string]map[string]PullRequest{}

	stack := []walkFrame{{tip, target}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for sha := f.start; ; {
			c, ok := ahead[sha]
			if !ok || visited[sha] {
				break
			}
			visited[sha] = true

			pr, found, err := d.findPR(c, f.chain, indexes)
			if err != nil {
				return res, err
			}
			second, hasSecond := "", len(c.Parents) > 1
			if hasSecond {
				second = c.Parents[1]
			}
			_, secondAhead := ahead[second]

			switch {
			case found && d.longLived(pr.HeadRef) && hasSecond && secondAhead:
				res.Carriers = append(res.Carriers, c.SHA)
				stack = append(stack, walkFrame{second, pr.HeadRef})
			case found && d.longLived(pr.HeadRef) && hasSecond:
				// A merged "main into dev" PR only brought in commits main has.
				res.BackMerges = append(res.BackMerges, c.SHA)
			case found:
				if !seenPR[pr.Number] {
					seenPR[pr.Number] = true
					res.PRs = append(res.PRs, pr)
				}
			case hasSecond && !secondAhead:
				res.BackMerges = append(res.BackMerges, c.SHA)
			case hasSecond && d.longLivedMerge(c.Message) != "":
				res.Carriers = append(res.Carriers, c.SHA)
				stack = append(stack, walkFrame{second, d.longLivedMerge(c.Message)})
			default:
				res.Unattributed = append(res.Unattributed, c)
			}

			if len(c.Parents) == 0 {
				break
			}
			sha = c.Parents[0]
		}
	}
	return res, nil
}

func (d *Detector) longLived(branch string) bool { return slices.Contains(d.LongLived, branch) }

// longLivedMerge returns the branch a "Merge branch 'x'" message names when x
// is long-lived, else "".
func (d *Detector) longLivedMerge(msg string) string {
	m := mergeBranchRe.FindStringSubmatch(msg)
	if m == nil || !d.longLived(m[1]) {
		return ""
	}
	return m[1]
}

// findPR tries GitHub's commit-to-PR lookup, then the PR number in the
// message, then an index of recently closed PRs; the lookup lags behind
// fresh merges and the message is only a hint.
func (d *Detector) findPR(c Commit, chain string, indexes map[string]map[string]PullRequest) (PullRequest, bool, error) {
	prs, err := d.Src.PullsForCommit(c.SHA)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return PullRequest{}, false, err
	}
	for _, p := range prs {
		if p.Merged && p.BaseRef == chain && p.MergeCommitSHA == c.SHA {
			return p, true, nil
		}
	}
	for _, p := range prs {
		if p.Merged && p.BaseRef == chain && !d.longLived(p.HeadRef) {
			return p, true, nil
		}
	}

	if n := messagePRNumber(c.Message); n > 0 {
		p, err := d.Src.Pull(n)
		switch {
		case err == nil:
			if p.Merged && p.MergeCommitSHA == c.SHA && d.longLived(p.BaseRef) {
				return p, true, nil
			}
		case !errors.Is(err, ErrNotFound):
			return PullRequest{}, false, err
		}
	}

	idx, ok := indexes[chain]
	if !ok {
		idx = map[string]PullRequest{}
		for page := 1; page <= indexPages; page++ {
			ps, err := d.Src.ClosedPullsByBase(chain, page)
			if err != nil {
				return PullRequest{}, false, err
			}
			if len(ps) == 0 {
				break
			}
			for _, p := range ps {
				if p.Merged && p.MergeCommitSHA != "" {
					idx[p.MergeCommitSHA] = p
				}
			}
		}
		indexes[chain] = idx
	}
	p, ok := idx[c.SHA]
	return p, ok, nil
}

func messagePRNumber(msg string) int {
	first := msg
	for i := range msg {
		if msg[i] == '\n' {
			first = msg[:i]
			break
		}
	}
	m := mergePRRe.FindStringSubmatch(first)
	if m == nil {
		m = squashPRRe.FindStringSubmatch(first)
	}
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

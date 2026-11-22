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

	attributed map[string]int // commit SHA -> number of the PR found for it, whatever its class
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
	chain string // the branch the chain's PRs were merged into
}

type walker struct {
	d       *Detector
	ahead   map[string]Commit
	res     walkResult
	stack   []walkFrame
	visited map[string]bool
	seenPR  map[int]bool
	indexes map[string]map[string]PullRequest
}

// walk follows the first-parent chain from tip and attributes every commit in
// ahead to a pull request. Whatever a merge brought in beside its first parent
// is walked the same way: a merge of another long-lived branch carries PRs
// that are also on the target, and a target fast-forwarded to a branch that
// had merged the target in keeps its previous history only there.
func (d *Detector) walk(target, tip string, ahead map[string]Commit) (walkResult, error) {
	w := &walker{
		d: d, ahead: ahead,
		res:     walkResult{attributed: map[string]int{}},
		stack:   []walkFrame{{tip, target}},
		visited: map[string]bool{},
		seenPR:  map[int]bool{},
		indexes: map[string]map[string]PullRequest{},
	}
	for len(w.stack) > 0 {
		f := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]

		for sha := f.start; ; {
			c, ok := w.ahead[sha]
			if !ok || w.visited[sha] {
				break
			}
			w.visited[sha] = true
			if err := w.classify(c, f.chain); err != nil {
				return w.res, err
			}
			if len(c.Parents) == 0 {
				break
			}
			sha = c.Parents[0]
		}
	}
	return w.res, nil
}

// classify records c in exactly one result list and queues every chain it
// merged in that still has to be walked.
func (w *walker) classify(c Commit, chain string) error {
	pr, found, err := w.d.findPR(c, chain, w.indexes)
	if err != nil {
		return err
	}
	if found {
		w.res.attributed[c.SHA] = pr.Number
	}
	merged := w.mergedIn(c)
	isMerge := len(c.Parents) > 1

	switch {
	case found && w.d.longLived(pr.HeadRef) && len(merged) > 0:
		w.res.Carriers = append(w.res.Carriers, c.SHA)
		w.descend(merged, pr.HeadRef)
	case found && w.d.longLived(pr.HeadRef) && isMerge:
		// A merged "main into dev" PR only brought in commits main has.
		w.res.BackMerges = append(w.res.BackMerges, c.SHA)
	case found:
		w.addPR(pr)
		// The PR's own head is judged by the shipped rules; anything else
		// merged in beside it is history the walk has not seen yet.
		w.descend(slices.DeleteFunc(merged, func(p string) bool { return p == pr.HeadSHA }), chain)
	case isMerge && len(merged) == 0:
		w.res.BackMerges = append(w.res.BackMerges, c.SHA)
	case w.d.longLivedMerge(c.Message) != "":
		w.res.Carriers = append(w.res.Carriers, c.SHA)
		w.descend(merged, w.d.longLivedMerge(c.Message))
	default:
		w.res.Unattributed = append(w.res.Unattributed, c)
		w.descend(merged, chain)
	}
	return nil
}

// mergedIn lists c's parents after the first that are inside the ahead set.
func (w *walker) mergedIn(c Commit) []string {
	var out []string
	for _, p := range c.Parents[min(1, len(c.Parents)):] {
		if _, ok := w.ahead[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

func (w *walker) descend(starts []string, chain string) {
	for _, s := range starts {
		w.stack = append(w.stack, walkFrame{s, chain})
	}
}

func (w *walker) addPR(pr PullRequest) {
	if !w.seenPR[pr.Number] {
		w.seenPR[pr.Number] = true
		w.res.PRs = append(w.res.PRs, pr)
	}
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

package main

import (
	"fmt"
	"path"
	"strconv"
)

// manifestName is the fixture's self-description: what was captured, from
// where, with which binary. A fixture without it cannot be reported on.
const manifestName = "capture.json"

// Manifest is written by capture and read by report. It records every command
// that produced the fixture so the whole comparison can be re-run — or
// disputed — from the fixture alone.
type Manifest struct {
	Repo string `json:"repo"`
	// CapturedAt and HewVersion date the numbers: renderer changes invalidate
	// the hew side, so a report has to say how old it is.
	CapturedAt string `json:"capturedAt"`
	HewVersion string `json:"hewVersion"`
	// OpenIssues is the denominator for per-issue figures, and Truncated
	// records that the repo has more open issues than one page returned — in
	// which case both sides undercount and the ratios are the honest part.
	OpenIssues int     `json:"openIssues"`
	Truncated  bool    `json:"truncated"`
	Entries    []Entry `json:"entries"`
}

// Entry is one read command compared against the raw-gh ways of answering the
// same question.
type Entry struct {
	// Command is the display label ("ready", "show #138").
	Command string `json:"command"`
	// PerIssue marks whole-tracker output, where tokens-per-open-issue is the
	// figure that generalizes beyond this fixture's size.
	PerIssue  bool       `json:"perIssue"`
	Hew       Capture    `json:"hew"`
	Baselines []Baseline `json:"baselines"`
}

// Baseline is one way to answer an entry's question without hew. Several
// commands can make up a single baseline: `gh issue view` plus a GraphQL call
// is one baseline, not two, because an agent needs both.
type Baseline struct {
	Name string `json:"name"`
	// Partial marks a baseline that cannot actually answer the question.
	// It is reported anyway — the cheap-but-wrong option is the one an agent
	// reaches for first, and its cost is worth knowing.
	Partial  bool      `json:"partial"`
	Note     string    `json:"note"`
	Captures []Capture `json:"captures"`
}

// Capture is one recorded command: what was run, and the file its stdout
// landed in.
type Capture struct {
	Argv []string `json:"argv"`
	File string   `json:"file"`
}

// Fixture is what capture needs to know about a repo: which issue to show,
// and which epic to roll up (zero when the repo has none).
type Fixture struct {
	Repo      string
	ShowIssue int
	EpicIssue int
	// Hew is the binary under measurement; QueryDir holds the baseline
	// GraphQL queries.
	Hew      string
	QueryDir string
}

// gh's own JSON field set for a list: the closest thing gh has to `hew ready`.
// Labels arrive as objects with id, description, and color whether or not the
// caller wants them — part of what the comparison is about.
const (
	ghListFields = "number,title,state,labels,assignees"
	ghViewFields = "number,title,body,state,stateReason,createdAt,labels,assignees,comments"
)

// entries builds the comparison. Baseline files are shared across entries by
// filename — `ready`, `list`, and `prime` all start from the same one open-issue
// query, and capture runs it once.
func (f Fixture) entries() []Entry {
	ghList := Capture{
		File: "gh-issue-list-open.json",
		Argv: []string{"gh", "issue", "list", "--repo", f.Repo, "--state", "open", "--limit", "200", "--json", ghListFields},
	}
	gqlOpen := Capture{
		File: "gh-graphql-open-issues.json",
		Argv: f.graphql("issues-open.graphql"),
	}

	// The state every whole-tracker read starts from. gh can only offer the
	// partial answer; the GraphQL query is the equivalent one.
	trackerBaselines := []Baseline{
		{
			Name:     "gh api graphql (open issues)",
			Captures: []Capture{gqlOpen},
		},
		{
			Name:     "gh issue list --json",
			Partial:  true,
			Note:     "gh cannot return blockedBy, parent, or sub-issues, so readiness and epic-ness are unanswerable from this output alone",
			Captures: []Capture{ghList},
		},
	}

	ents := []Entry{
		{
			Command:   "ready",
			PerIssue:  true,
			Hew:       Capture{File: "hew-ready.txt", Argv: f.hew("ready")},
			Baselines: trackerBaselines,
		},
		{
			Command:   "list",
			PerIssue:  true,
			Hew:       Capture{File: "hew-list.txt", Argv: f.hew("list")},
			Baselines: trackerBaselines,
		},
		{
			Command:   "list --json",
			PerIssue:  true,
			Hew:       Capture{File: "hew-list-json.ndjson", Argv: f.hew("list", "--json")},
			Baselines: trackerBaselines,
		},
		{
			Command:  "prime",
			PerIssue: false,
			Hew:      Capture{File: "hew-prime.txt", Argv: f.hew("prime")},
			Baselines: []Baseline{{
				Name: "gh api graphql (open issues)",
				Note: "understates the gap: the primer also carries the conventions and command cheatsheet, " +
					"which a raw-gh agent has to keep as hand-written prose in CLAUDE.md",
				Captures: []Capture{gqlOpen},
			}},
		},
	}

	if f.ShowIssue > 0 {
		n := strconv.Itoa(f.ShowIssue)
		view := Capture{
			File: "gh-issue-view-" + n + ".json",
			Argv: []string{"gh", "issue", "view", n, "--repo", f.Repo, "--json", ghViewFields},
		}
		deps := Capture{
			File: "gh-graphql-issue-deps-" + n + ".json",
			Argv: f.graphql("issue-deps.graphql", "-F", "number="+n),
		}
		ents = append(ents, Entry{
			Command: "show #" + n,
			Hew:     Capture{File: "hew-show-" + n + ".txt", Argv: f.hew("show", n)},
			Baselines: []Baseline{
				{
					Name:     "gh issue view --json + gh api graphql",
					Captures: []Capture{view, deps},
				},
				{
					Name:     "gh issue view --json",
					Partial:  true,
					Note:     "no parent, sub-issues, or blockers",
					Captures: []Capture{view},
				},
			},
		})
	}

	if f.EpicIssue > 0 {
		n := strconv.Itoa(f.EpicIssue)
		ents = append(ents, Entry{
			Command: "epic status " + n,
			Hew:     Capture{File: "hew-epic-status-" + n + ".txt", Argv: f.hew("epic", "status", n)},
			Baselines: []Baseline{{
				Name:     "gh api graphql (epic + children)",
				Captures: []Capture{{File: "gh-graphql-epic-" + n + ".json", Argv: f.graphql("epic.graphql", "-F", "number="+n)}},
			}},
		})
	}

	return ents
}

func (f Fixture) hew(args ...string) []string {
	return append([]string{f.Hew}, append(args, "--repo", f.Repo)...)
}

// graphql builds a gh api call whose query is read from a file, so the
// recorded argv stays short and is literally re-runnable from the module root.
func (f Fixture) graphql(query string, extra ...string) []string {
	argv := []string{
		"gh", "api", "graphql",
		"-F", "owner=" + f.owner(),
		"-F", "name=" + f.name(),
		"-F", "query=@" + path.Join(f.QueryDir, query),
	}
	return append(argv, extra...)
}

func (f Fixture) owner() string { owner, _ := splitRepo(f.Repo); return owner }
func (f Fixture) name() string  { _, name := splitRepo(f.Repo); return name }

func splitRepo(repo string) (owner, name string) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			return repo[:i], repo[i+1:]
		}
	}
	return repo, ""
}

// validate rejects a fixture that would produce a meaningless capture, before
// any API call is made.
func (f Fixture) validate() error {
	owner, name := splitRepo(f.Repo)
	if owner == "" || name == "" {
		return fmt.Errorf("--repo must be owner/name, got %q", f.Repo)
	}
	if f.ShowIssue < 0 || f.EpicIssue < 0 {
		return fmt.Errorf("issue numbers must be positive")
	}
	if f.QueryDir == "" {
		return fmt.Errorf("--queries must not be empty")
	}
	return nil
}

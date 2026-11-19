package daemon

import (
	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// The library tags only tracker.Issue for JSON (it is the --json schema).
// Every other result type marshals with Go field names, so each is mapped to
// the camelCase shape the frontend sees. This file is the cost of that gap.

type bodyIn struct {
	Where    string   `json:"where"`
	Problem  string   `json:"problem"`
	Goal     string   `json:"goal"`
	Fix      string   `json:"fix"`
	Approach string   `json:"approach"`
	DoneWhen []string `json:"doneWhen"`
	Text     string   `json:"text"`
}

func (b bodyIn) body(discoveredFrom int) tracker.Body {
	return tracker.Body{
		Sections: tracker.Sections{
			Where: b.Where, Problem: b.Problem, Goal: b.Goal,
			Fix: b.Fix, Approach: b.Approach, DoneWhen: b.DoneWhen,
		},
		Text:           b.Text,
		DiscoveredFrom: discoveredFrom,
	}
}

type findingOut struct {
	Kind   string   `json:"kind"`
	Tokens []string `json:"tokens"`
}

func findingsOut(fs []tracker.Finding) []findingOut {
	out := make([]findingOut, 0, len(fs))
	for _, f := range fs {
		out = append(out, findingOut{Kind: string(f.Kind), Tokens: orEmpty(f.Tokens)})
	}
	return out
}

type warningOut struct {
	Kind    string `json:"kind"`
	Issue   int    `json:"issue"`
	Cycle   []int  `json:"cycle"`
	Total   int    `json:"total"`
	Fetched int    `json:"fetched"`
}

func warningsOut(ws []tracker.Warning) []warningOut {
	out := make([]warningOut, 0, len(ws))
	for _, w := range ws {
		out = append(out, warningOut{Kind: string(w.Kind), Issue: w.Issue, Cycle: orEmpty(w.Cycle), Total: w.Total, Fetched: w.Fetched})
	}
	return out
}

type textFindingOut struct {
	Issue       int    `json:"issue"`
	Source      string `json:"source"`
	Comment     int    `json:"comment"`
	ZeroWidth   bool   `json:"zeroWidth"`
	BidiControl bool   `json:"bidiControl"`
	UnicodeTags bool   `json:"unicodeTags"`
	Confusable  bool   `json:"confusable"`
}

func textFindingsOut(fs []tracker.TextFinding) []textFindingOut {
	out := make([]textFindingOut, 0, len(fs))
	for _, f := range fs {
		out = append(out, textFindingOut(f))
	}
	return out
}

type detailOut struct {
	Issue        tracker.Issue    `json:"issue"`
	Children     []tracker.Issue  `json:"children"`
	Next         *int             `json:"next"`
	TextFindings []textFindingOut `json:"textFindings"`
	Warnings     []warningOut     `json:"warnings"`
}

func detailFrom(d tracker.IssueDetail) detailOut {
	return detailOut{
		Issue: d.Issue, Children: orEmpty(d.Children), Next: d.Next,
		TextFindings: textFindingsOut(d.TextFindings), Warnings: warningsOut(d.Warnings),
	}
}

type edgeOut struct {
	Kind string `json:"kind"`
	To   int    `json:"to"`
}

type createOut struct {
	Issue    tracker.Issue `json:"issue"`
	Edges    []edgeOut     `json:"edges"`
	Findings []findingOut  `json:"findings"`
}

func createFrom(r tracker.CreateResult) createOut {
	edges := make([]edgeOut, 0, len(r.Edges))
	for _, e := range r.Edges {
		edges = append(edges, edgeOut{Kind: string(e.Kind), To: e.To})
	}
	return createOut{Issue: r.Issue, Edges: edges, Findings: findingsOut(r.Findings)}
}

type listOut struct {
	Issues  []tracker.Issue `json:"issues"`
	Total   int             `json:"total"`
	Fetched int             `json:"fetched"`
}

type primerOut struct {
	Ready           []tracker.Issue `json:"ready"`
	ReadyTotal      int             `json:"readyTotal"`
	OpenTotal       int             `json:"openTotal"`
	InProgress      []tracker.Issue `json:"inProgress"`
	InProgressTotal int             `json:"inProgressTotal"`
	Epics           []tracker.Issue `json:"epics"`
	EpicsTotal      int             `json:"epicsTotal"`
	Untriaged       int             `json:"untriaged"`
	Warnings        []warningOut    `json:"warnings"`
	Conventions     string          `json:"conventions"`
}

func primerFrom(p tracker.PrimerResult) primerOut {
	return primerOut{
		Ready: orEmpty(p.Ready), ReadyTotal: p.ReadyTotal, OpenTotal: p.OpenTotal,
		InProgress: orEmpty(p.InProgress), InProgressTotal: p.InProgressTotal,
		Epics: orEmpty(p.Epics), EpicsTotal: p.EpicsTotal,
		Untriaged: p.Untriaged, Warnings: warningsOut(p.Warnings), Conventions: p.Conventions,
	}
}

type prWarningOut struct {
	Kind      string   `json:"kind"`
	Issue     int      `json:"issue"`
	Issues    []int    `json:"issues"`
	Branch    string   `json:"branch"`
	Assignees []string `json:"assignees"`
	Actor     string   `json:"actor"`
	Groups    [][]int  `json:"groups"`
}

// prWarningsOut keeps every subject a warning carries, the way warningsOut does
// for tracker warnings: a bare kind cannot name the issue a claim conflict is
// about or who holds it.
func prWarningsOut(ws []pr.Warning) []prWarningOut {
	out := make([]prWarningOut, 0, len(ws))
	for _, w := range ws {
		out = append(out, prWarningOut{
			Kind: string(w.Kind), Issue: w.Issue, Issues: orEmpty(w.Issues), Branch: w.Branch,
			Assignees: orEmpty(w.Assignees), Actor: w.Actor, Groups: orEmpty(w.Groups),
		})
	}
	return out
}

type prOut struct {
	Number      int            `json:"number"`
	URL         string         `json:"url"`
	Draft       bool           `json:"draft"`
	Fixes       int            `json:"fixes"`
	FixedIssues []int          `json:"fixedIssues"`
	Head        string         `json:"head"`
	Base        string         `json:"base"`
	Title       string         `json:"title"`
	Warnings    []prWarningOut `json:"warnings"`
	Findings    []findingOut   `json:"findings"`
}

func prFrom(r pr.CreateResult) prOut {
	return prOut{
		Number: r.Number, URL: r.URL, Draft: r.Draft, Fixes: r.Fixes, FixedIssues: orEmpty(r.FixedIssues),
		Head: r.Head, Base: r.Base, Title: r.Title, Warnings: prWarningsOut(r.Warnings), Findings: findingsOut(r.Findings),
	}
}

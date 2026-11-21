package tracker

import (
	"context"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// ListFilter narrows a snapshot's list view. The zero value keeps every
// fetched, triaged issue in list order.
type ListFilter struct {
	// Label keeps issues carrying the label, compared case-insensitively as
	// GitHub and the CLI's --label both do.
	Label string
	// Epic keeps issues whose parent is this epic. Zero applies no epic
	// filter.
	Epic int
}

// List answers `hew list` from one snapshot: the fetched issues in list order
// — ready work first, then claimed, blocked, epics and closed — with the
// filter applied. Untriaged issues are omitted in every state and under every
// filter; Triage is the deliberate API that returns their text.
func (s *Snapshot) List(filter ListFilter) []Issue {
	issues := slices.Clone(s.issues)
	model.SortForList(issues)
	return issuesFromModel(model.List(issues, model.ListFilter{Label: filter.Label, Epic: filter.Epic}), s.bodies)
}

// TextFinding is one field's advisory character findings. Source attributes
// the finding to the title, the body, or one displayed comment, so a reader
// knows which part of a long issue to inspect. The four flags are independent
// and may all be false on a clean field; findings never filter, rewrite or
// reject the text.
type TextFinding struct {
	// Issue is the issue the text belongs to.
	Issue int `json:"issue"`
	// Source is TextFindingTitle, TextFindingBody or TextFindingComment.
	Source string `json:"source"`
	// Comment indexes Issue.Comments when Source is TextFindingComment: the
	// comments actually fetched and displayed, not the server-side thread.
	Comment     int  `json:"comment"`
	ZeroWidth   bool `json:"zeroWidth"`
	BidiControl bool `json:"bidiControl"`
	UnicodeTags bool `json:"unicodeTags"`
	Confusable  bool `json:"confusable"`
}

// Sources a TextFinding can name.
const (
	TextFindingTitle   = "title"
	TextFindingBody    = "body"
	TextFindingComment = "comment"
)

// IssueDetail is one issue's full view: the issue itself, an epic's
// parent-backlinked children and next workable child, the advisory findings
// for its text, and the snapshot warnings that bear on the view.
type IssueDetail struct {
	// Issue is the subject, with body, comments and close reason.
	Issue Issue `json:"issue"`
	// Children is an epic's full child set, oldest first, including closed
	// and untriaged children: naming the epic is the explicit retrieval path.
	// It is a list of rows, so it carries no bodies; it is nil for a
	// non-epic.
	Children []Issue `json:"children"`
	// Next is the epic's next workable child, nil for a non-epic or when the
	// sequence is dry.
	Next *int `json:"next"`
	// TextFindings annotates the title, body and displayed comments. Comments
	// are scanned because Detail is a live single-issue read and carries them.
	TextFindings []TextFinding `json:"textFindings"`
	// Warnings is the snapshot's completeness notices for an epic read; nil
	// for a non-epic, which fetches no snapshot.
	Warnings []Warning `json:"warnings"`
}

// Detail is the show read: one live issue in any state, with body and recent
// comments. An epic's view also fetches both states to resolve its children
// from parent backlinks — complete even when the epic's sub-issue connection
// was capped — and names the next workable child. Children keep every existing
// relationship, including an idea attached outside hew; the next-child selector
// skips ideas, because the epic's sequence is implementation work. An issue
// number named by the caller is the deliberate retrieval path, so untriaged
// text is returned here.
func (t *Tracker) Detail(ctx context.Context, number int) (IssueDetail, error) {
	issue, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return IssueDetail{}, asReadError(err)
	}
	detail := IssueDetail{
		Issue:        issueFromModel(issue, true),
		TextFindings: textFindings(issue.Number, model.ScanIssue(issue)),
	}
	if !issue.IsEpic() {
		return detail, nil
	}
	states := []State{StateOpen, StateClosed}
	issues, err := t.store.ListIssues(ctx, []gh.IssueState{gh.StateOpen, gh.StateClosed})
	if err != nil {
		return IssueDetail{}, asReadError(err)
	}
	snapshot := newSnapshot(issues, states, false)
	children := model.Children(snapshot.issues, number)
	detail.Children = issuesFromModel(children, false)
	detail.Warnings = snapshot.Warnings()
	if next, ok := model.EpicNext(children); ok {
		number := next.Number
		detail.Next = &number
	}
	return detail, nil
}

// textFindings maps the scan to the public shape, keeping one entry per
// flagged field in display order.
func textFindings(issue int, findings []model.SourcedFindings) []TextFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]TextFinding, len(findings))
	for n, finding := range findings {
		out[n] = TextFinding{
			Issue:       issue,
			Source:      finding.Source,
			Comment:     finding.Comment,
			ZeroWidth:   finding.ZeroWidth,
			BidiControl: finding.BidiControl,
			UnicodeTags: finding.UnicodeTags,
			Confusable:  finding.Confusable,
		}
	}
	return out
}

// SearchRequest is the dedup search before filing discovered work: terms
// matched by the backend across open and closed issues.
type SearchRequest struct {
	// Terms are the backend's search terms, passed through as written after
	// trimming. Empty terms are invalid.
	Terms string
	// States narrows the search to these states before the backend's cap, so
	// the cap bounds the matches asked for. Empty means open and closed —
	// unlike SnapshotOptions.States, where empty means open only. An unknown
	// state is an invalid-input error.
	States []State
	// Bodies carries each match's body and comments. The zero value omits
	// them: dedup reads a title, a number and its labels, and a caller that
	// needs the text reads the issue.
	Bodies bool
}

// SearchResult is a search listing: the triaged matches in the backend's
// relevance order, plus the backend's full match count and how many matches
// it fetched. Total above Fetched means the result was capped. Issues carries
// bodies only when SearchRequest.Bodies asked for them.
type SearchResult struct {
	Issues  []Issue `json:"issues"`
	Total   int     `json:"total"`
	Fetched int     `json:"fetched"`
}

// Search is the dedup read: a repo-scoped text search over open and closed
// issues, in the backend's relevance order (GitHub's best match, the local
// store's title-first). Untriaged matches are omitted — Triage's search form
// is the other half of dedup — so Issues may be shorter than Fetched.
func (t *Tracker) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	terms := strings.TrimSpace(req.Terms)
	if terms == "" {
		return SearchResult{}, &InvalidInputError{Field: "terms", Reason: "must not be empty"}
	}
	var fetch []gh.IssueState
	if len(req.States) > 0 {
		var err error
		if _, fetch, err = resolveStates(req.States); err != nil {
			return SearchResult{}, err
		}
	}
	issues, total, err := t.store.SearchIssues(ctx, terms, fetch)
	if err != nil {
		return SearchResult{}, asReadError(err)
	}
	return SearchResult{
		Issues:  issuesFromModel(triagedOnly(issues), req.Bodies),
		Total:   total,
		Fetched: len(issues),
	}, nil
}

// TriageRequest selects the triage view. Empty Search is the open untriaged
// queue; a non-empty Search is the untriaged half of dedup across both states.
type TriageRequest struct {
	Search string
	// Bodies carries each untriaged issue's body and comments. The zero
	// value omits them; the advisory findings are computed from the fetched
	// text regardless, so an annotator can leave it unset. `hew triage` sets
	// it because its renderers scan the text themselves.
	Bodies bool
}

// TriageResult is a triage listing: the untriaged matches, the completeness
// counts for a search (the queue form is uncapped, so its Total equals
// Fetched), and the advisory text findings for each match. Issues carries
// bodies only when TriageRequest.Bodies asked for them; the findings are
// computed from the fetched text either way.
type TriageResult struct {
	Issues       []Issue       `json:"issues"`
	Total        int           `json:"total"`
	Fetched      int           `json:"fetched"`
	TextFindings []TextFinding `json:"textFindings"`
}

// Triage lists issues missing their priority or type label — the deliberate
// API that returns untriaged text to a caller that asked for it. With Search
// empty it is the open queue, oldest first; with Search set it is the
// untriaged matches among the backend's search results, spanning both states.
func (t *Tracker) Triage(ctx context.Context, req TriageRequest) (TriageResult, error) {
	if strings.TrimSpace(req.Search) == "" {
		if req.Search != "" {
			return TriageResult{}, &InvalidInputError{Field: "terms", Reason: "must not be empty"}
		}
		issues, err := t.store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
		if err != nil {
			return TriageResult{}, asReadError(err)
		}
		matches := model.UntriagedIssues(issues)
		return TriageResult{
			Issues:       issuesFromModel(matches, req.Bodies),
			Total:        len(matches),
			Fetched:      len(matches),
			TextFindings: triageFindings(matches),
		}, nil
	}
	issues, total, err := t.store.SearchIssues(ctx, strings.TrimSpace(req.Search), nil)
	if err != nil {
		return TriageResult{}, asReadError(err)
	}
	matches := untriagedOnly(issues)
	return TriageResult{
		Issues:       issuesFromModel(matches, req.Bodies),
		Total:        total,
		Fetched:      len(issues),
		TextFindings: triageFindings(matches),
	}, nil
}

// triageFindings scans the fields triage prints: title and body, attributed
// separately. A triage read does not fetch comments.
func triageFindings(issues []model.Issue) []TextFinding {
	var out []TextFinding
	for _, issue := range issues {
		scanned := model.Issue{Number: issue.Number, Title: issue.Title, Body: issue.Body}
		out = append(out, textFindings(issue.Number, model.ScanIssue(scanned))...)
	}
	return out
}

// triagedOnly returns the issues that carry both a priority and a type,
// preserving the backend's order.
func triagedOnly(issues []model.Issue) []model.Issue {
	out := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if !issue.Untriaged() {
			out = append(out, issue)
		}
	}
	return out
}

// untriagedOnly is triagedOnly's inverse, preserving order.
func untriagedOnly(issues []model.Issue) []model.Issue {
	out := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Untriaged() {
			out = append(out, issue)
		}
	}
	return out
}

package local

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

const (
	// searchCap matches the GitHub client's cap: callers warn when the
	// reported total exceeds what came back.
	searchCap = 20
	// commentsShown is how many recent comments GetIssue returns, as on GitHub.
	commentsShown = 5
)

func (s *Store) Viewer(ctx context.Context) (string, error) { return s.viewer, nil }

func (s *Store) ListIssues(ctx context.Context, states []gh.IssueState) ([]model.Issue, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []model.Issue
	for _, i := range d.build() {
		if slices.Contains(states, gh.IssueState(i.State)) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *Store) GetIssue(ctx context.Context, number int) (model.Issue, error) {
	d, err := s.load()
	if err != nil {
		return model.Issue{}, err
	}
	return d.view(number)
}

// view builds one issue with its recent comments, the detail only show needs.
func (d *data) view(number int) (model.Issue, error) {
	rec, err := d.find(number)
	if err != nil {
		return model.Issue{}, err
	}
	for _, i := range d.build() {
		if i.Number != number {
			continue
		}
		i.CommentsTotal = len(rec.Comments)
		for _, c := range rec.Comments[max(0, len(rec.Comments)-commentsShown):] {
			i.Comments = append(i.Comments, model.Comment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body})
		}
		return i, nil
	}
	return model.Issue{}, fmt.Errorf("issue #%d not found", number)
}

// SearchIssues matches issues containing every whitespace-separated term,
// case-insensitively, in the title, body or labels, across both states.
// Issues whose title carries all the terms come first. Qualifiers such as
// label:bug are GitHub syntax and are matched as literal text here.
func (s *Store) SearchIssues(ctx context.Context, terms string) ([]model.Issue, int, error) {
	words := strings.Fields(strings.ToLower(terms))
	if len(words) == 0 {
		return nil, 0, nil
	}
	d, err := s.load()
	if err != nil {
		return nil, 0, err
	}
	var inTitle, elsewhere []model.Issue
	for _, i := range d.build() {
		title := strings.ToLower(i.Title)
		switch {
		case containsAll(title, words):
			inTitle = append(inTitle, i)
		case containsAll(title+"\n"+strings.ToLower(i.Body)+"\n"+strings.ToLower(strings.Join(i.Labels, " ")), words):
			elsewhere = append(elsewhere, i)
		}
	}
	matches := append(inTitle, elsewhere...)
	total := len(matches)
	return matches[:min(total, searchCap)], total, nil
}

func containsAll(haystack string, words []string) bool {
	return !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(haystack, w) })
}

// CreateIssue stores raw issue fields. User-facing title and area validation
// belongs to the shared CLI write path, so local and GitHub commands refuse
// the same inputs. Labels here can include convention labels such as the
// in-progress claim; they are not necessarily user-supplied areas.
func (s *Store) CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error) {
	var created model.Issue
	err := s.update(ctx, func(d *data) error {
		next := 1
		if n := len(d.issues); n > 0 {
			// Issues are kept sorted and never removed, so numbers are never reused.
			next = d.issues[n-1].Number + 1
		}
		rec := &issueRec{Number: next, Title: title, Body: body, State: string(gh.StateOpen), CreatedAt: s.now()}
		for _, l := range labels {
			rec.Labels = appendUniqueFold(rec.Labels, d.labelName(l, rec.Labels))
		}
		d.issues = append(d.issues, rec)
		var err error
		created, err = d.view(next)
		return err
	})
	return created, err
}

func (s *Store) EditTitle(ctx context.Context, number int, title string) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error { r.Title = title; return nil })
}

func (s *Store) EditBody(ctx context.Context, number int, body string) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error { r.Body = body; return nil })
}

func (s *Store) AddLabels(ctx context.Context, number int, labels []string) error {
	return s.edit(ctx, number, func(d *data, r *issueRec) error {
		for _, l := range labels {
			r.Labels = appendUniqueFold(r.Labels, d.labelName(l, r.Labels))
		}
		return nil
	})
}

// RemoveLabel treats an absent label as success, as the GitHub client does.
// Label names are case-insensitive, so the removal matches the same way.
func (s *Store) RemoveLabel(ctx context.Context, number int, label string) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error {
		r.Labels = slices.DeleteFunc(r.Labels, func(l string) bool { return strings.EqualFold(l, label) })
		return nil
	})
}

// ClaimIssue is the atomic claim: under the store's lock it checks the issue
// is unclaimed and writes the assignee and the in-progress label together, so
// two concurrent starts cannot both succeed. It is the Claimer capability
// Start uses instead of check-then-act.
func (s *Store) ClaimIssue(ctx context.Context, number int, login string, force bool) error {
	return s.edit(ctx, number, func(d *data, r *issueRec) error {
		if r.State != string(gh.StateOpen) {
			return fmt.Errorf("#%d is closed", number)
		}
		// The same test start's pre-read applies (model.Issue.Claimed), so the
		// two sides cannot disagree about what counts as claimed.
		claimed := len(r.Assignees) > 0 || slices.ContainsFunc(r.Labels, model.IsInProgressLabel)
		if claimed && !force {
			return fmt.Errorf("%w: #%d", gh.ErrClaimConflict, number)
		}
		if force {
			r.Assignees = nil
		}
		r.Assignees = appendUnique(r.Assignees, login)
		r.Labels = appendUniqueFold(r.Labels, d.labelName(model.InProgressLabel, r.Labels))
		return nil
	})
}

func (s *Store) AddAssignee(ctx context.Context, number int, login string) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error {
		r.Assignees = appendUnique(r.Assignees, login)
		return nil
	})
}

func (s *Store) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error {
		r.Assignees = slices.DeleteFunc(r.Assignees, func(l string) bool { return slices.Contains(logins, l) })
		return nil
	})
}

func (s *Store) Comment(ctx context.Context, number int, body string) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error {
		r.Comments = append(r.Comments, commentRec{Author: s.viewer, CreatedAt: s.now(), Body: body})
		return nil
	})
}

func (s *Store) CloseIssue(ctx context.Context, number int, reason gh.CloseReason) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error {
		r.State, r.StateReason = string(gh.StateClosed), string(reason)
		return nil
	})
}

// ReopenIssue records REOPENED as the state reason rather than clearing the
// close reason, which is what GitHub does.
func (s *Store) ReopenIssue(ctx context.Context, number int) error {
	return s.edit(ctx, number, func(_ *data, r *issueRec) error {
		r.State, r.StateReason = string(gh.StateOpen), "REOPENED"
		return nil
	})
}

// AddBlockedBy refuses a self-block and a direct two-issue cycle, the cases
// GitHub's API refuses. Longer cycles are accepted here too: the commands run
// the transitive check before they write, and the read path reports cycles.
func (s *Store) AddBlockedBy(ctx context.Context, number, blockingNumber int) error {
	return s.edit(ctx, number, func(d *data, r *issueRec) error {
		blocker, err := d.find(blockingNumber)
		if err != nil {
			return err
		}
		if number == blockingNumber {
			return fmt.Errorf("issue #%d cannot block itself", number)
		}
		if slices.Contains(blocker.BlockedBy, number) {
			return fmt.Errorf("issues #%d and #%d would block each other", number, blockingNumber)
		}
		if !slices.Contains(r.BlockedBy, blockingNumber) {
			r.BlockedBy = append(r.BlockedBy, blockingNumber)
		}
		return nil
	})
}

func (s *Store) RemoveBlockedBy(ctx context.Context, number, blockingNumber int) error {
	return s.edit(ctx, number, func(d *data, r *issueRec) error {
		if _, err := d.find(blockingNumber); err != nil {
			return err
		}
		r.BlockedBy = slices.DeleteFunc(r.BlockedBy, func(n int) bool { return n == blockingNumber })
		return nil
	})
}

// AddSubIssue makes child a sub-issue of parent. An issue has one parent, so
// an existing different parent is an error unless replaceParent is set. A
// parent that is the child or one of its descendants is refused: epics are
// trees.
func (s *Store) AddSubIssue(ctx context.Context, parentNumber, childNumber int, replaceParent bool) error {
	return s.edit(ctx, childNumber, func(d *data, child *issueRec) error {
		parent, err := d.find(parentNumber)
		if err != nil {
			return err
		}
		// Bounded by the issue count so a hand-edited file that already holds a
		// cycle cannot hang the walk.
		parentOf := func(r *issueRec) *issueRec {
			p, _ := d.find(r.Parent) // no parent (0) finds nothing, ending the walk
			return p
		}
		for hops, up := 0, parent; up != nil && hops <= len(d.issues); hops, up = hops+1, parentOf(up) {
			if up.Number == childNumber {
				return fmt.Errorf("#%d cannot be a sub-issue of #%d: that would make a cycle", childNumber, parentNumber)
			}
		}
		if child.Parent != 0 && child.Parent != parentNumber && !replaceParent {
			return fmt.Errorf("#%d already has a parent", childNumber)
		}
		child.Parent = parentNumber
		return nil
	})
}

func (s *Store) RemoveSubIssue(ctx context.Context, parentNumber, childNumber int) error {
	return s.edit(ctx, childNumber, func(d *data, child *issueRec) error {
		if _, err := d.find(parentNumber); err != nil {
			return err
		}
		if child.Parent != parentNumber {
			return fmt.Errorf("#%d is not a sub-issue of #%d", childNumber, parentNumber)
		}
		child.Parent = 0
		return nil
	})
}

// ListLabels returns the labels created explicitly plus any in use on an
// issue, so a label applied by hand still shows up.
func (s *Store) ListLabels(ctx context.Context) ([]gh.Label, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	out := slices.Clone(d.labels)
	for _, rec := range d.issues {
		for _, name := range rec.Labels {
			if !slices.ContainsFunc(out, func(l gh.Label) bool { return strings.EqualFold(l.Name, name) }) {
				out = append(out, gh.Label{Name: name})
			}
		}
	}
	return out, nil
}

// CreateLabel records a label's cosmetics. Creating one that exists is a
// no-op rather than an error, so bootstrapping is safe to repeat; label names
// are case-insensitive, so an existing spelling wins.
func (s *Store) CreateLabel(ctx context.Context, label gh.Label) error {
	return s.update(ctx, func(d *data) error {
		if !slices.ContainsFunc(d.labels, func(l gh.Label) bool { return strings.EqualFold(l.Name, label.Name) }) {
			d.labels = append(d.labels, label)
		}
		return nil
	})
}

// edit is update for the common case: find one issue, change it. A missing
// issue fails before anything is written.
func (s *Store) edit(ctx context.Context, number int, fn func(d *data, r *issueRec) error) error {
	return s.update(ctx, func(d *data) error {
		rec, err := d.find(number)
		if err != nil {
			return err
		}
		return fn(d, rec)
	})
}

func appendUnique[T comparable](list []T, v T) []T {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// appendUniqueFold is appendUnique for names GitHub treats case-insensitively
// (labels): the spelling already present wins.
func appendUniqueFold(list []string, v string) []string {
	if slices.ContainsFunc(list, func(l string) bool { return strings.EqualFold(l, v) }) {
		return list
	}
	return append(list, v)
}

// labelName is the spelling a label is stored under. GitHub labels are
// repo-scoped and case-insensitive, so a label already created or in use
// anywhere decides the casing; a name nobody has used keeps its spelling.
func (d *data) labelName(name string, onIssue []string) string {
	for _, l := range d.labels {
		if strings.EqualFold(l.Name, name) {
			return l.Name
		}
	}
	for _, l := range onIssue {
		if strings.EqualFold(l, name) {
			return l
		}
	}
	for _, rec := range d.issues {
		for _, l := range rec.Labels {
			if strings.EqualFold(l, name) {
				return l
			}
		}
	}
	return name
}

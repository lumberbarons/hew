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

func (s *Store) CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error) {
	var created model.Issue
	err := s.update(func(d *data) error {
		next := 1
		if n := len(d.issues); n > 0 {
			// Issues are kept sorted and never removed, so numbers are never reused.
			next = d.issues[n-1].Number + 1
		}
		rec := &issueRec{Number: next, Title: title, Body: body, State: string(gh.StateOpen), CreatedAt: s.now()}
		for _, l := range labels {
			rec.Labels = appendUnique(rec.Labels, l)
		}
		d.issues = append(d.issues, rec)
		var err error
		created, err = d.view(next)
		return err
	})
	return created, err
}

func (s *Store) EditTitle(ctx context.Context, number int, title string) error {
	return s.edit(number, func(_ *data, r *issueRec) error { r.Title = title; return nil })
}

func (s *Store) EditBody(ctx context.Context, number int, body string) error {
	return s.edit(number, func(_ *data, r *issueRec) error { r.Body = body; return nil })
}

func (s *Store) AddLabels(ctx context.Context, number int, labels []string) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		for _, l := range labels {
			r.Labels = appendUnique(r.Labels, l)
		}
		return nil
	})
}

// RemoveLabel treats an absent label as success, as the GitHub client does.
func (s *Store) RemoveLabel(ctx context.Context, number int, label string) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		r.Labels = slices.DeleteFunc(r.Labels, func(l string) bool { return l == label })
		return nil
	})
}

func (s *Store) AddAssignee(ctx context.Context, number int, login string) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		r.Assignees = appendUnique(r.Assignees, login)
		return nil
	})
}

func (s *Store) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		r.Assignees = slices.DeleteFunc(r.Assignees, func(l string) bool { return slices.Contains(logins, l) })
		return nil
	})
}

func (s *Store) Comment(ctx context.Context, number int, body string) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		r.Comments = append(r.Comments, commentRec{Author: s.viewer, CreatedAt: s.now(), Body: body})
		return nil
	})
}

func (s *Store) CloseIssue(ctx context.Context, number int, reason gh.CloseReason) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		r.State, r.StateReason = string(gh.StateClosed), string(reason)
		return nil
	})
}

// ReopenIssue records REOPENED as the state reason rather than clearing the
// close reason, which is what GitHub does.
func (s *Store) ReopenIssue(ctx context.Context, number int) error {
	return s.edit(number, func(_ *data, r *issueRec) error {
		r.State, r.StateReason = string(gh.StateOpen), "REOPENED"
		return nil
	})
}

// AddBlockedBy refuses a self-block and a direct two-issue cycle, the cases
// GitHub's API refuses. Longer cycles are accepted here too: the commands run
// the transitive check before they write, and the read path reports cycles.
func (s *Store) AddBlockedBy(ctx context.Context, number, blockingNumber int) error {
	return s.edit(number, func(d *data, r *issueRec) error {
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
	return s.edit(number, func(d *data, r *issueRec) error {
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
	return s.edit(childNumber, func(d *data, child *issueRec) error {
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
	return s.edit(childNumber, func(d *data, child *issueRec) error {
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
			if !slices.ContainsFunc(out, func(l gh.Label) bool { return l.Name == name }) {
				out = append(out, gh.Label{Name: name})
			}
		}
	}
	return out, nil
}

// CreateLabel records a label's cosmetics. Creating one that exists is a
// no-op rather than an error, so bootstrapping is safe to repeat.
func (s *Store) CreateLabel(ctx context.Context, label gh.Label) error {
	return s.update(func(d *data) error {
		if !slices.ContainsFunc(d.labels, func(l gh.Label) bool { return l.Name == label.Name }) {
			d.labels = append(d.labels, label)
		}
		return nil
	})
}

// edit is update for the common case: find one issue, change it. A missing
// issue fails before anything is written.
func (s *Store) edit(number int, fn func(d *data, r *issueRec) error) error {
	return s.update(func(d *data) error {
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

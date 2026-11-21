package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// EditResult reports what Edit carried out. Applied names the request fields
// that landed, in the order they were applied — a field whose requested value
// already held is named too, because the edit's intent covered it. ParentNoop
// reports a NoParent request on an issue that had no parent: the requested
// state already held and nothing was written for it.
type EditResult struct {
	Applied    []string `json:"applied"`
	ParentNoop bool     `json:"parentNoop"`
}

// Edit applies a partial edit to one issue. Fields the request leaves unset
// are left alone; the optional pointer fields carry presence, so an explicitly
// empty title, body, priority or type is a refusal rather than an omission.
//
// Every requested change is validated before the first write — the field
// values, the area names, the parent target, and the closed-target guard — so
// a refusal changes nothing. Priority and type swaps enforce the conventions'
// one-label invariant over the whole type vocabulary, idea included; areas are
// normalized (trimmed, de-duplicated) and may not name a convention label. An
// edit never touches a claim. A parent move or
// removal only rewires the edited issue's own parent link: its sub-issues and
// its old parent's other children stay where they are.
//
// The refusals are typed: InvalidInputError names the request field at fault,
// ClosedError reports a closed target unless AllowClosed is set, and
// NotFoundError reports an issue or a parent the tracker does not have. A
// write that fails after an earlier one landed is a PartialEditError naming
// the fields applied, the field that failed and the backend cause.
func (t *Tracker) Edit(ctx context.Context, req EditRequest) (EditResult, error) {
	if req.empty() {
		return EditResult{}, &InvalidInputError{Field: "edit", Reason: "nothing to change; set at least one field"}
	}
	if req.Parent != nil && req.NoParent {
		return EditResult{}, &InvalidInputError{Field: "parent", Reason: "cannot be combined with noParent"}
	}
	if req.Parent != nil && *req.Parent == req.Issue {
		return EditResult{}, &InvalidInputError{Field: "parent", Reason: "is the issue being edited"}
	}
	var priority model.Priority
	if req.Priority != nil {
		p, ok := model.ParsePriority(*req.Priority)
		if !ok {
			return EditResult{}, &InvalidInputError{Field: "priority", Reason: "must be P0..P4"}
		}
		priority = p
	}
	if req.Type != nil && !model.IsType(*req.Type) {
		return EditResult{}, &InvalidInputError{Field: "type", Reason: "must be one of " + strings.Join(model.Types, "|")}
	}
	if req.Title != nil {
		if err := ValidateTitle(*req.Title); err != nil {
			return EditResult{}, err
		}
	}
	if req.Body != nil && strings.TrimSpace(*req.Body) == "" {
		return EditResult{}, &InvalidInputError{Field: "body", Reason: "must not be empty"}
	}
	addAreas, err := NormalizeAreas(req.AddAreas)
	if err != nil {
		return EditResult{}, fieldError(err, "addAreas")
	}
	if err := validateAreaNames(req.RemoveAreas); err != nil {
		return EditResult{}, fieldError(err, "removeAreas")
	}

	issue, err := t.store.GetIssue(ctx, req.Issue)
	if err != nil {
		return EditResult{}, asReadError(err)
	}
	if !issue.IsOpen() && !req.AllowClosed {
		return EditResult{}, &ClosedError{Issue: req.Issue, Reason: issue.StateReason}
	}
	// Resolve the parent before the first write, like every other reference an
	// edit writes: a typo must not leave the earlier fields applied.
	var parent model.Issue
	if req.Parent != nil {
		var err error
		parent, err = t.store.GetIssue(ctx, *req.Parent)
		if err != nil {
			return EditResult{}, asReadError(err)
		}
	}
	// The idea type takes no epic position. Check the edit's result before the
	// first write: a type change to idea may only land when no relationship or
	// marker would survive it, and a new parent edge may not touch an idea on
	// either end. A NoParent in the same request is part of the result, which
	// is how a maintainer repairs an existing overlap.
	resultingType, _ := issue.Type()
	if req.Type != nil {
		resultingType = *req.Type
	}
	if req.Type != nil && *req.Type == model.IdeaType {
		retained := issue
		switch {
		case req.NoParent:
			retained.Parent = nil
		case req.Parent != nil:
			retained.Parent = &model.Ref{Number: *req.Parent, State: parent.State}
		}
		if conflict := retained.IdeaEpicConflict(); conflict != "" {
			return EditResult{}, &InvalidInputError{
				Field:  "type",
				Reason: fmt.Sprintf("cannot classify #%d as an idea while %s; detach the relationship or change its type first", req.Issue, conflict),
			}
		}
	}
	if req.Parent != nil {
		if resultingType == model.IdeaType {
			return EditResult{}, &InvalidInputError{Field: "parent", Reason: ideaChildRefusal}
		}
		if typ, _ := parent.Type(); typ == model.IdeaType {
			return EditResult{}, &InvalidInputError{
				Field:  "parent",
				Reason: fmt.Sprintf("#%d is an idea; %s", parent.Number, ideaParentRefusal),
			}
		}
	}

	var result EditResult
	step := func(field string, err error) error {
		if err != nil {
			if len(result.Applied) == 0 {
				return asWriteError(err)
			}
			return &PartialEditError{
				Issue:   req.Issue,
				Applied: slices.Clone(result.Applied),
				Failed:  field,
				Err:     asWriteError(err),
			}
		}
		result.Applied = append(result.Applied, field)
		return nil
	}
	if req.Priority != nil {
		if err := step("priority", swapPriority(ctx, t.store, issue, priority)); err != nil {
			return EditResult{}, err
		}
	}
	if req.Type != nil {
		if err := step("type", swapType(ctx, t.store, issue, *req.Type)); err != nil {
			return EditResult{}, err
		}
	}
	if len(addAreas) > 0 {
		if err := step("addAreas", t.store.AddLabels(ctx, req.Issue, addAreas)); err != nil {
			return EditResult{}, err
		}
	}
	for _, area := range req.RemoveAreas {
		if err := step("removeAreas", t.store.RemoveLabel(ctx, req.Issue, area)); err != nil {
			return EditResult{}, err
		}
	}
	if req.Title != nil {
		if err := step("title", t.store.EditTitle(ctx, req.Issue, *req.Title)); err != nil {
			return EditResult{}, err
		}
	}
	if req.Body != nil {
		if err := step("body", t.store.EditBody(ctx, req.Issue, *req.Body)); err != nil {
			return EditResult{}, err
		}
	}
	if req.Parent != nil {
		// AddSubIssue with replace moves the issue when it already has one.
		if err := step("parent", t.store.AddSubIssue(ctx, *req.Parent, req.Issue, true)); err != nil {
			return EditResult{}, err
		}
	}
	if req.NoParent {
		if issue.Parent == nil {
			result.ParentNoop = true
		} else if err := step("noParent", t.store.RemoveSubIssue(ctx, issue.Parent.Number, req.Issue)); err != nil {
			return EditResult{}, err
		}
	}
	return result, nil
}

// empty reports a request that names no change. AllowClosed is a modifier, not
// a change, so it does not count.
func (r EditRequest) empty() bool {
	return r.Priority == nil && r.Type == nil && r.Title == nil && r.Body == nil &&
		len(r.AddAreas) == 0 && len(r.RemoveAreas) == 0 && r.Parent == nil && !r.NoParent
}

// fieldError retargets a validator's InvalidInputError at the request field
// that carried the offending value, so a caller reads an EditRequest name
// rather than the shared area vocabulary.
func fieldError(err error, field string) error {
	var invalid *InvalidInputError
	if errors.As(err, &invalid) {
		return &InvalidInputError{Field: field, Reason: invalid.Reason}
	}
	return err
}

// validateAreaNames checks names that must not be convention labels, without
// NormalizeAreas's trim, length or comma rules: a removal has to be able to
// name an oversized area an older tracker stored, so it can be repaired before
// a local migration.
func validateAreaNames(areas []string) error {
	for _, area := range areas {
		if err := ValidateAreaLabel(area); err != nil {
			return err
		}
	}
	return nil
}

// swapType enforces the one-type-label invariant: remove the others, add the
// target if absent. It is swapPriority's type half, and the reason an edit
// never stacks a second type on an issue that carried a stray one.
func swapType(ctx context.Context, store gh.IssueStore, issue model.Issue, typ string) error {
	for _, l := range issue.Labels {
		if _, ok := model.LabelType(l); ok && !strings.EqualFold(l, typ) {
			if err := store.RemoveLabel(ctx, issue.Number, l); err != nil {
				return err
			}
		}
	}
	if !issue.HasLabel(typ) {
		return store.AddLabels(ctx, issue.Number, []string{typ})
	}
	return nil
}

package model

import (
	"slices"
	"testing"
)

func listNumbers(issues []Issue) []int {
	out := make([]int, len(issues))
	for i, issue := range issues {
		out[i] = issue.Number
	}
	return out
}

// List is the list read's policy: untriaged text never crosses it, the label
// match is case-insensitive, the epic filter follows the parent backlink, and
// the caller's order is preserved.
func TestListFiltersWithoutReordering(t *testing.T) {
	ready := Issue{Number: 1, Title: "Ready", Labels: []string{"P2", "bug", "parser"}}
	half := Issue{Number: 2, Title: "Half", Labels: []string{"P2", "parser"}}
	other := Issue{Number: 3, Title: "Other", Labels: []string{"P1", "task"}}
	child := Issue{Number: 4, Title: "Child", Labels: []string{"P2", "task"}, Parent: &Ref{Number: 10, State: "OPEN"}}
	epicChild := Issue{Number: 5, Title: "Parented", Labels: []string{"P2", "bug"}, Parent: &Ref{Number: 20, State: "OPEN"}}
	issues := []Issue{ready, half, other, child, epicChild}

	if got, want := listNumbers(List(issues, ListFilter{})), []int{1, 3, 4, 5}; !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
	if got, want := listNumbers(List(issues, ListFilter{Label: "PARSER"})), []int{1}; !slices.Equal(got, want) {
		t.Errorf("List(label) = %v, want %v", got, want)
	}
	if got, want := listNumbers(List(issues, ListFilter{Epic: 10})), []int{4}; !slices.Equal(got, want) {
		t.Errorf("List(epic) = %v, want %v", got, want)
	}
	if got := List(issues, ListFilter{Label: "bug", Epic: 10}); len(got) != 0 {
		t.Errorf("List(label+epic) = %v, want no issue satisfying both", listNumbers(got))
	}

	// SortForList supplies the order; List must preserve it rather than
	// re-deriving one.
	sorted := slices.Clone(issues)
	SortForList(sorted)
	if got, want := listNumbers(List(sorted, ListFilter{})), []int{3, 1, 4, 5}; !slices.Equal(got, want) {
		t.Errorf("List after SortForList = %v, want %v", got, want)
	}
}

package consumer_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lumberbarons/hew/migrate"
	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// shape is one exported result type's wire contract: the exact JSON keys it
// marshals, which of those must be an empty collection on a zero value (never
// null), and which may legitimately be null.
type shape struct {
	name string
	// value is a populated value of the type, so the non-empty branch of every
	// collection normalizer is exercised too.
	value any
	// keys is the complete, sorted key set the type marshals.
	keys []string
	// empty names the keys that a zero value must render as [] or {}, so a
	// consumer never has to tell null from empty.
	empty []string
	// nullable names the keys a zero value may render as null.
	nullable []string
}

// TestPublicResultTypesMarshalAsCamelCaseJSON is the wire contract the daemon
// and every other consumer relies on: an exported result type marshals to the
// same camelCase shape tracker.Issue already carries, and a nil slice or map is
// an empty collection rather than null. Without this a server needs a mapping
// layer per type, which is what daemon/dto.go used to be.
func TestPublicResultTypesMarshalAsCamelCaseJSON(t *testing.T) {
	for _, c := range resultShapes() {
		t.Run(c.name, func(t *testing.T) {
			assertShape(t, c, c.value)

			zero := reflect.New(reflect.TypeOf(c.value)).Elem().Interface()
			got := assertShape(t, c, zero)
			for _, key := range c.empty {
				switch v := got[key].(type) {
				case []any:
					if len(v) != 0 {
						t.Errorf("zero %s[%q] = %v, want []", c.name, key, v)
					}
				case map[string]any:
					if len(v) != 0 {
						t.Errorf("zero %s[%q] = %v, want {}", c.name, key, v)
					}
				default:
					t.Errorf("zero %s[%q] = %#v, want an empty collection", c.name, key, got[key])
				}
			}
			for key, v := range got {
				if v != nil {
					continue
				}
				if !containsString(c.nullable, key) {
					t.Errorf("zero %s[%q] is null; collections must be empty and only %v may be null", c.name, key, c.nullable)
				}
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// assertShape marshals v and checks its key set against the shape's contract.
func assertShape(t *testing.T, c shape, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", c.name, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", c.name, err, raw)
	}
	keys := make([]string, 0, len(got))
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, c.keys) {
		t.Errorf("%s keys = %v, want %v\nJSON: %s", c.name, keys, c.keys, raw)
	}
	return got
}

// resultShapes is every exported result and warning type the library returns.
// The list is the contract: a new result type that marshals with Go field names
// or a null collection is missing here and from its own json tags.
func resultShapes() []shape {
	return []shape{
		{
			name:  "tracker.Target",
			value: tracker.Target{Backend: tracker.BackendLocal, Repo: "o/r", Dir: "/repo/.hew", MainCheckout: "/repo", ID: "id"},
			keys:  []string{"backend", "dir", "id", "mainCheckout", "repo"},
		},
		{
			name:  "tracker.RateLimit",
			value: tracker.RateLimit{Cost: 1, Remaining: 2, ResetAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			keys:  []string{"cost", "remaining", "resetAt"},
		},
		{
			name:  "tracker.StartResult",
			value: tracker.StartResult{Issue: tracker.Issue{Number: 1}, Claimant: "alice"},
			keys:  []string{"claimant", "issue"},
		},
		{
			name: "tracker.CreateResult",
			value: tracker.CreateResult{
				Issue:    tracker.Issue{Number: 1},
				Edges:    []tracker.CreatedEdge{{Kind: tracker.BlockedByEdge, To: 2}},
				Findings: []tracker.Finding{{Kind: tracker.FindingUnmarkedCode, Tokens: []string{"x"}}},
			},
			keys:  []string{"edges", "findings", "issue"},
			empty: []string{"edges", "findings"},
		},
		{
			name:  "tracker.CreatedEdge",
			value: tracker.CreatedEdge{Kind: tracker.BlockedByEdge, To: 2},
			keys:  []string{"kind", "to"},
		},
		{
			name:  "tracker.EditResult",
			value: tracker.EditResult{Applied: []string{"title"}, ParentNoop: true},
			keys:  []string{"applied", "parentNoop"},
			empty: []string{"applied"},
		},
		{
			name:  "tracker.ReopenResult",
			value: tracker.ReopenResult{Reopened: true, ReleasedLabel: true, ReleasedAssignees: []string{"alice"}},
			keys:  []string{"releasedAssignees", "releasedLabel", "reopened"},
			empty: []string{"releasedAssignees"},
		},
		{
			name:  "tracker.CloseResult",
			value: tracker.CloseResult{StateReason: "COMPLETED"},
			keys:  []string{"stateReason"},
		},
		{
			name:  "tracker.BlockResult",
			value: tracker.BlockResult{Added: []int{2}, Existing: []int{3}},
			keys:  []string{"added", "existing"},
			empty: []string{"added", "existing"},
		},
		{
			name:  "tracker.UnblockResult",
			value: tracker.UnblockResult{Removed: []int{2}, Missing: []int{3}},
			keys:  []string{"missing", "removed"},
			empty: []string{"missing", "removed"},
		},
		{
			name: "tracker.InitResult",
			value: tracker.InitResult{
				Backend: tracker.BackendLocal, Path: "/repo/.hew",
				CreatedLabels: []string{"bug"}, IssuesRecreated: true, Notices: []string{"created"},
			},
			keys:  []string{"backend", "createdLabels", "issuesRecreated", "notices", "path"},
			empty: []string{"createdLabels", "notices"},
		},
		{
			name: "tracker.ApplyResult",
			value: tracker.ApplyResult{
				DryRun: true, Created: 1, Wired: 2, Unwired: 3,
				Warnings:       []string{"dropped"},
				Mapping:        map[string]int{"a": 1},
				Applied:        []tracker.AppliedEntry{{Key: "a", Issue: 1, Created: true}},
				WouldCreate:    []tracker.PlannedEntry{{Key: "b", Title: "B"}},
				AlreadyCreated: map[string]int{"c": 3},
			},
			keys: []string{
				"alreadyCreated", "applied", "created", "dryRun", "mapping", "unwired",
				"warnings", "wired", "wouldCreate",
			},
			empty: []string{"applied", "alreadyCreated", "mapping", "warnings", "wouldCreate"},
		},
		{
			name:  "tracker.AppliedEntry",
			value: tracker.AppliedEntry{Key: "a", Issue: 1, Created: true},
			keys:  []string{"created", "issue", "key"},
		},
		{
			name: "tracker.PlannedEntry",
			value: tracker.PlannedEntry{
				Key: "a", Priority: "P2", Type: "task", Title: "A",
				Areas: []string{"cli"}, Parent: new(string), BlockedBy: []string{"#1"},
			},
			keys:     []string{"areas", "blockedBy", "key", "parent", "priority", "title", "type"},
			empty:    []string{"areas", "blockedBy"},
			nullable: []string{"parent"},
		},
		{
			name: "tracker.IssueDetail",
			value: tracker.IssueDetail{
				Issue:        tracker.Issue{Number: 1},
				Children:     []tracker.Issue{{Number: 2}},
				TextFindings: []tracker.TextFinding{{Issue: 1, Source: tracker.TextFindingTitle}},
				Warnings:     []tracker.Warning{{Kind: tracker.WarnDependencyCycle, Cycle: []int{1, 2, 1}}},
			},
			keys:     []string{"children", "issue", "next", "textFindings", "warnings"},
			empty:    []string{"children", "textFindings", "warnings"},
			nullable: []string{"next"},
		},
		{
			name:  "tracker.SearchResult",
			value: tracker.SearchResult{Issues: []tracker.Issue{{Number: 1}}, Total: 2, Fetched: 1},
			keys:  []string{"fetched", "issues", "total"},
			empty: []string{"issues"},
		},
		{
			name: "tracker.TriageResult",
			value: tracker.TriageResult{
				Issues:       []tracker.Issue{{Number: 1}},
				Total:        2,
				Fetched:      1,
				TextFindings: []tracker.TextFinding{{Issue: 1, Source: tracker.TextFindingBody}},
			},
			keys:  []string{"fetched", "issues", "textFindings", "total"},
			empty: []string{"issues", "textFindings"},
		},
		{
			name: "tracker.PrimerResult",
			value: tracker.PrimerResult{
				Ready:           []tracker.Issue{{Number: 1}},
				ReadyTotal:      1,
				OpenTotal:       2,
				InProgress:      []tracker.Issue{{Number: 2}},
				InProgressTotal: 1,
				Epics:           []tracker.Issue{{Number: 3}},
				EpicsTotal:      1,
				Untriaged:       1,
				Warnings:        []tracker.Warning{{Kind: tracker.WarnMultiPriority}},
				Conventions:     "workflow",
			},
			keys: []string{
				"conventions", "epics", "epicsTotal", "inProgress", "inProgressTotal",
				"openTotal", "ready", "readyTotal", "untriaged", "warnings",
			},
			empty: []string{"epics", "inProgress", "ready", "warnings"},
		},
		{
			name:  "tracker.ComposedBody",
			value: tracker.ComposedBody{Body: "text", Findings: []tracker.Finding{{Kind: tracker.FindingUnmarkedCode}}},
			keys:  []string{"body", "findings"},
			empty: []string{"findings"},
		},
		{
			name:  "tracker.BodyCheck",
			value: tracker.BodyCheck{Findings: []tracker.Finding{{Kind: tracker.FindingUnmarkedCode}}},
			keys:  []string{"findings"},
			empty: []string{"findings"},
		},
		{
			name:  "tracker.Warning",
			value: tracker.Warning{Kind: tracker.WarnBlockersCapped, Issue: 1, Cycle: []int{1, 2, 1}, Total: 3, Fetched: 1},
			keys:  []string{"cycle", "fetched", "issue", "kind", "total"},
			empty: []string{"cycle"},
		},
		{
			name:  "tracker.Finding",
			value: tracker.Finding{Kind: tracker.FindingUnmarkedCode, Tokens: []string{"x"}},
			keys:  []string{"kind", "tokens"},
			empty: []string{"tokens"},
		},
		{
			name: "tracker.TextFinding",
			value: tracker.TextFinding{
				Issue: 1, Source: tracker.TextFindingComment, Comment: 2,
				ZeroWidth: true, BidiControl: true, UnicodeTags: true, Confusable: true,
			},
			keys: []string{"bidiControl", "comment", "confusable", "issue", "source", "unicodeTags", "zeroWidth"},
		},
		{
			name: "pr.CreateResult",
			value: pr.CreateResult{
				Number: 7, URL: "https://example/7", Draft: true, Fixes: 1,
				FixedIssues: []int{1}, Head: "feat/1-x", Base: "main", Title: "feat: x",
				Warnings: []pr.Warning{{Kind: pr.WarnMissingTesting, Issues: []int{1}}},
				Findings: []tracker.Finding{{Kind: tracker.FindingUnmarkedCode}},
			},
			keys: []string{
				"base", "draft", "findings", "fixedIssues", "fixes", "head", "number",
				"title", "url", "warnings",
			},
			empty: []string{"findings", "fixedIssues", "warnings"},
		},
		{
			name: "pr.Warning",
			value: pr.Warning{
				Kind: pr.WarnUnclaimed, Issue: 1, Issues: []int{1}, Branch: "feat/1-x",
				Assignees: []string{"alice"}, Actor: "bob", Groups: [][]int{{1}},
			},
			keys:  []string{"actor", "assignees", "branch", "groups", "issue", "issues", "kind"},
			empty: []string{"assignees", "groups", "issues"},
		},
		{
			name: "migrate.Result",
			value: migrate.Result{
				DryRun: true, Created: 1, Wired: 2, Closed: 3, Relinked: 4, Failed: 5,
				Selected: 6, Skipped: 7, Backend: tracker.BackendGitHub,
				Warnings:       []migrate.Warning{{Kind: migrate.WarnEdgeDropped, Key: "k", Issue: 1, Message: "dropped"}},
				Mapping:        map[string]int{"k": 1},
				Plan:           []migrate.PlanStep{{Key: "k", BlockedBy: []string{"j"}}},
				WouldCreate:    []migrate.PlannedItem{{Key: "k", Title: "K"}},
				AlreadyCreated: map[string]int{"j": 2},
			},
			keys: []string{
				"alreadyCreated", "backend", "closed", "created", "dryRun", "failed",
				"mapping", "plan", "relinked", "selected", "skipped", "warnings",
				"wired", "wouldCreate",
			},
			empty: []string{"alreadyCreated", "mapping", "plan", "warnings", "wouldCreate"},
		},
		{
			name:  "migrate.PlanStep",
			value: migrate.PlanStep{Key: "k", Migrated: 1, Title: "K", Priority: "P2", Type: "task", Parent: "p", BlockedBy: []string{"j"}, InProgress: true, ThenClose: true},
			keys:  []string{"blockedBy", "inProgress", "key", "migrated", "parent", "priority", "thenClose", "title", "type"},
			empty: []string{"blockedBy"},
		},
		{
			name:  "migrate.PlannedItem",
			value: migrate.PlannedItem{Key: "k", Title: "K"},
			keys:  []string{"key", "title"},
		},
		{
			name:  "migrate.Warning",
			value: migrate.Warning{Kind: migrate.WarnBodyEdited, Key: "k", Issue: 1, Message: "edited"},
			keys:  []string{"issue", "key", "kind", "message"},
		},
	}
}

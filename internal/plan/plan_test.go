package plan

import (
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func TestParse(t *testing.T) {
	fixture := `{"id":"epic1","title":"Voltgo support","type":"epic","priority":"P1","body":"### Goal\n\nstuff"}

{"id":"scaffold","title":"Scaffold","type":"task","parent":"epic1","areas":["ble"]}
{"title":"Collector","type":"enhancement","priority":"P3","parent":"epic1","blocked-by":["scaffold",42],"discovered-from":7}
`
	entries, err := Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d", len(entries))
	}

	epic := entries[0]
	if epic.Key() != "epic1" || !epic.IsEpic() || epic.Priority != model.P1 || epic.Line != 1 {
		t.Errorf("epic = %+v", epic)
	}
	if !strings.Contains(epic.Body, "### Goal") {
		t.Errorf("body = %q", epic.Body)
	}

	scaffold := entries[1]
	if scaffold.Priority != model.DefaultPriority {
		t.Errorf("default priority = %v", scaffold.Priority)
	}
	if scaffold.Parent == nil || scaffold.Parent.ID != "epic1" || scaffold.Parent.Number != 0 {
		t.Errorf("parent = %+v", scaffold.Parent)
	}
	if len(scaffold.Areas) != 1 || scaffold.Areas[0] != "ble" {
		t.Errorf("areas = %v", scaffold.Areas)
	}

	// The id-less entry is keyed by its line — blank lines still count.
	collector := entries[2]
	if collector.Key() != "line:4" {
		t.Errorf("key = %q", collector.Key())
	}
	if len(collector.BlockedBy) != 2 ||
		collector.BlockedBy[0] != (Ref{ID: "scaffold"}) ||
		collector.BlockedBy[1] != (Ref{Number: 42}) {
		t.Errorf("blockedBy = %+v", collector.BlockedBy)
	}
	if collector.DiscoveredFrom != 7 {
		t.Errorf("discoveredFrom = %d", collector.DiscoveredFrom)
	}
}

func TestParseSections(t *testing.T) {
	fixture := `{"title":"x","type":"task","where":"internal/cli","goal":"Ship","approach":"Care","done-when":["a","b"]}` + "\n"
	entries, err := Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	s := entries[0].Sections
	if s.Where != "internal/cli" || s.Goal != "Ship" || s.Approach != "Care" || len(s.DoneWhen) != 2 {
		t.Errorf("sections = %+v", s)
	}
}

func TestParseEmpty(t *testing.T) {
	for _, in := range []string{"", "\n\n"} {
		entries, err := Parse(strings.NewReader(in))
		if err != nil || len(entries) != 0 {
			t.Errorf("Parse(%q) = %v, %v", in, entries, err)
		}
	}
}

func TestRefString(t *testing.T) {
	if got := (Ref{ID: "epic1"}).String(); got != "epic1" {
		t.Errorf("local ref = %q", got)
	}
	if got := (Ref{Number: 42}).String(); got != "#42" {
		t.Errorf("numeric ref = %q", got)
	}
}

func TestParseErrors(t *testing.T) {
	task := func(fields string) string {
		return `{"title":"x","type":"task"` + fields + "}\n"
	}
	tests := []struct {
		name, fixture, want string
	}{
		{"malformed json", "{not json\n", "line 1"},
		{"unknown field", task(`,"blockedby":[1]`), "unknown field"},
		{"missing title", `{"type":"task"}` + "\n", "title is required"},
		{"missing type", `{"title":"x"}` + "\n", "type must be one of bug|enhancement|task|epic"},
		{"bad type", `{"title":"x","type":"feature"}` + "\n", "type must be"},
		{"bad priority", task(`,"priority":"P9"`), "priority must be P0..P4"},
		{"negative discovered-from", task(`,"discovered-from":-1`), "discovered-from"},
		{"area is a priority", task(`,"areas":["P1"]`), `area "P1" collides`},
		{"area is a type", task(`,"areas":["bug"]`), `area "bug" collides`},
		{"area is in-progress", task(`,"areas":["in-progress"]`), `area "in-progress" collides`},
		{"problem and goal", task(`,"problem":"p","goal":"g"`), "problem and goal are mutually exclusive"},
		{"fix and approach", task(`,"fix":"f","approach":"a"`), "fix and approach are mutually exclusive"},
		{"empty done-when item", task(`,"done-when":[" "]`), "done-when items cannot be empty"},
		{"body and sections", task(`,"body":"b","goal":"g"`), "body and section fields"},
		{"duplicate id", task(`,"id":"a"`) + task(`,"id":"a"`), `line 2: duplicate id "a" (first used on line 1)`},
		{"reserved id", task(`,"id":"line:9"`), "reserved"},
		{"unknown parent", task(`,"parent":"nope"`), `parent "nope" does not match any entry id`},
		{"unknown blocker", task(`,"blocked-by":["nope"]`), `blocked-by "nope" does not match any entry id`},
		{"self parent", task(`,"id":"a","parent":"a"`), "cannot be its own parent"},
		{"self blocker", task(`,"id":"a","blocked-by":["a"]`), "cannot be its own blocked-by"},
		{"duplicate blocker", task(`,"id":"a"`) + task(`,"blocked-by":["a","a"]`), "duplicate blocked-by a"},
		{"zero issue number", task(`,"blocked-by":[0]`), "issue number must be positive"},
		{"bad ref type", task(`,"blocked-by":[true]`), "reference must be a local id (string) or an issue number"},
		{"empty ref id", task(`,"parent":""`), "reference id cannot be empty"},
		{
			"dependency cycle",
			task(`,"id":"a","blocked-by":["c"]`) + task(`,"id":"b","blocked-by":["a"]`) + task(`,"id":"c","blocked-by":["b"]`),
			"dependency cycle in plan: a → c → b → a",
		},
		{
			"parent cycle",
			task(`,"id":"a","parent":"b"`) + task(`,"id":"b","parent":"a"`),
			"parent cycle in plan: a → b → a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.fixture))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// A cycle through existing issue numbers is impossible (existing issues
// can't reference unborn entries), so numeric refs never trip the cycle
// check even when local ids alongside them do form chains.
func TestParseNumericRefsNoCycle(t *testing.T) {
	fixture := `{"id":"a","title":"x","type":"task","blocked-by":[10]}
{"id":"b","title":"y","type":"task","blocked-by":["a",10]}
`
	if _, err := Parse(strings.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
}

package daemon

import (
	"encoding/json"

	"github.com/lumberbarons/hew/tracker"
)

// The library's result types carry the wire shape themselves (tracker/json.go,
// pr/json.go, migrate/json.go): camelCase keys and empty collections instead of
// null. The handlers therefore return a result value directly. What is left
// here is the request side and the two responses that are compositions rather
// than one library result.

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

// checkOut is a validation answer: whether the call was accepted, plus the
// advisory findings that never refuse it. There is no library result type for
// "valid, with advice" — the body-check result carries only the findings — so
// the route composes the two.
type checkOut struct {
	OK       bool              `json:"ok"`
	Findings []tracker.Finding `json:"findings"`
}

// MarshalJSON keeps an accepted body's advice an empty array rather than null,
// the same guarantee the library's result types give.
func (c checkOut) MarshalJSON() ([]byte, error) {
	type plain checkOut
	out := plain(c)
	out.Findings = orEmpty(out.Findings)
	return json.Marshal(out)
}

// boardOut is the landing view: one snapshot sliced into the sections `hew
// list` orders, plus the untriaged count. It is a composition of several
// library reads, not a re-mapping of one result.
type boardOut struct {
	Ready     []tracker.Issue   `json:"ready"`
	Claimed   []tracker.Issue   `json:"claimed"`
	Blocked   []tracker.Issue   `json:"blocked"`
	Epics     []tracker.Issue   `json:"epics"`
	Untriaged int               `json:"untriaged"`
	Warnings  []tracker.Warning `json:"warnings"`
}

// MarshalJSON keeps every section and the warnings an empty array rather than
// null, the same guarantee the library's result types give.
func (b boardOut) MarshalJSON() ([]byte, error) {
	type plain boardOut
	out := plain(b)
	out.Ready = orEmpty(out.Ready)
	out.Claimed = orEmpty(out.Claimed)
	out.Blocked = orEmpty(out.Blocked)
	out.Epics = orEmpty(out.Epics)
	out.Warnings = orEmpty(out.Warnings)
	return json.Marshal(out)
}

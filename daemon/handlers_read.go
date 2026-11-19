package daemon

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/lumberbarons/hew/tracker"
)

type configOut struct {
	Backend      string `json:"backend"`
	Repo         string `json:"repo"`
	Identity     string `json:"identity"`
	Dir          string `json:"dir"`
	MainCheckout string `json:"mainCheckout"`
	ID           string `json:"id"`
	// ActorEditable is true where X-Hew-Actor changes who a request acts as:
	// only a local tracker. On GitHub every request is the authenticated login.
	ActorEditable bool `json:"actorEditable"`
	PullRequests  bool `json:"pullRequests"`
}

func (s *Server) config(q *request[none]) (reply, error) {
	return ok(configOut{
		Backend: string(q.tr.Backend()), Repo: q.tr.Repo(), Identity: q.tr.Identity(),
		Dir: s.target.Dir, MainCheckout: s.target.MainCheckout, ID: s.target.ID,
		ActorEditable: q.tr.Backend() == tracker.BackendLocal,
		PullRequests:  s.prc != nil,
	}), nil
}

func (s *Server) primer(q *request[none]) (reply, error) {
	p, err := q.tr.Primer(q.ctx)
	if err != nil {
		return reply{}, err
	}
	return ok(primerFrom(p)), nil
}

type boardOut struct {
	Ready     []tracker.Issue `json:"ready"`
	Claimed   []tracker.Issue `json:"claimed"`
	Blocked   []tracker.Issue `json:"blocked"`
	Epics     []tracker.Issue `json:"epics"`
	Untriaged int             `json:"untriaged"`
	Warnings  []warningOut    `json:"warnings"`
}

// board is the landing view: one snapshot sliced into the sections `hew list`
// orders. Untriaged issues appear only as a count, never as text; triage is
// the one read that returns them.
func (s *Server) board(q *request[none]) (reply, error) {
	snap, err := q.tr.Snapshot(q.ctx, tracker.SnapshotOptions{})
	if err != nil {
		return reply{}, err
	}
	return ok(boardOut{
		Ready: lean(snap.Ready()), Claimed: lean(snap.Claimed()),
		Blocked: lean(snap.Blocked()), Epics: lean(snap.Epics()),
		Untriaged: snap.Untriaged(), Warnings: warningsOut(snap.Warnings()),
	}), nil
}

// limited applies ?limit=N, ignoring a missing, malformed or non-positive one.
func limited(issues []tracker.Issue, query url.Values) []tracker.Issue {
	if lim, err := strconv.Atoi(query.Get("limit")); err == nil && lim > 0 && lim < len(issues) {
		return issues[:lim]
	}
	return issues
}

func (s *Server) ready(q *request[none]) (reply, error) {
	snap, err := q.tr.Snapshot(q.ctx, tracker.SnapshotOptions{})
	if err != nil {
		return reply{}, err
	}
	return ok(lean(limited(snap.Ready(), q.query))), nil
}

// states parses ?state=open|closed|all. The snapshot default (open only) and
// the search default (open and closed) differ in the library, so each handler
// passes its own fallback.
func states(q string, fallback []tracker.State) ([]tracker.State, error) {
	switch q {
	case "":
		return fallback, nil
	case "open":
		return []tracker.State{tracker.StateOpen}, nil
	case "closed":
		return []tracker.State{tracker.StateClosed}, nil
	case "all":
		return []tracker.State{tracker.StateOpen, tracker.StateClosed}, nil
	}
	return nil, &tracker.InvalidInputError{Field: "state", Reason: "must be open, closed or all"}
}

func (s *Server) list(q *request[none]) (reply, error) {
	st, err := states(q.query.Get("state"), nil)
	if err != nil {
		return reply{}, err
	}
	filter := tracker.ListFilter{Label: q.query.Get("label")}
	if e := q.query.Get("epic"); e != "" {
		if filter.Epic, err = strconv.Atoi(e); err != nil || filter.Epic <= 0 {
			return reply{}, &tracker.InvalidInputError{Field: "epic", Reason: "must be a positive issue number"}
		}
	}
	snap, err := q.tr.Snapshot(q.ctx, tracker.SnapshotOptions{States: st})
	if err != nil {
		return reply{}, err
	}
	return ok(lean(limited(snap.List(filter), q.query))), nil
}

func (s *Server) detail(q *request[none]) (reply, error) {
	d, err := q.tr.Detail(q.ctx, q.n)
	if err != nil {
		return reply{}, err
	}
	return ok(detailFrom(d)), nil
}

func (s *Server) epics(q *request[none]) (reply, error) {
	st, err := states(q.query.Get("state"), nil)
	if err != nil {
		return reply{}, err
	}
	snap, err := q.tr.Snapshot(q.ctx, tracker.SnapshotOptions{States: st})
	if err != nil {
		return reply{}, err
	}
	return ok(lean(snap.Epics())), nil
}

func (s *Server) search(q *request[none]) (reply, error) {
	st, err := states(q.query.Get("state"), nil)
	if err != nil {
		return reply{}, err
	}
	res, err := q.tr.Search(q.ctx, tracker.SearchRequest{Terms: strings.TrimSpace(q.query.Get("q")), States: st})
	if err != nil {
		return reply{}, err
	}
	return ok(listOut{Issues: lean(res.Issues), Total: res.Total, Fetched: res.Fetched}), nil
}

type triageOut struct {
	listOut
	TextFindings []textFindingOut `json:"textFindings"`
}

// triage is the one route that returns untriaged text. The UI says so before
// it calls it, as the CLI's primer does.
func (s *Server) triage(q *request[none]) (reply, error) {
	res, err := q.tr.Triage(q.ctx, tracker.TriageRequest{Search: strings.TrimSpace(q.query.Get("q"))})
	if err != nil {
		return reply{}, err
	}
	return ok(triageOut{
		listOut:      listOut{Issues: lean(res.Issues), Total: res.Total, Fetched: res.Fetched},
		TextFindings: textFindingsOut(res.TextFindings),
	}), nil
}

package daemon

import (
	"net/http"
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

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	tr := s.acting(r)
	writeJSON(w, http.StatusOK, configOut{
		Backend: string(tr.Backend()), Repo: tr.Repo(), Identity: tr.Identity(),
		Dir: s.target.Dir, MainCheckout: s.target.MainCheckout, ID: s.target.ID,
		ActorEditable: tr.Backend() == tracker.BackendLocal,
		PullRequests:  s.prc != nil,
	})
}

func (s *Server) handlePrimer(w http.ResponseWriter, r *http.Request) {
	p, err := s.tr.Primer(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, primerFrom(p))
}

type boardOut struct {
	Ready     []tracker.Issue `json:"ready"`
	Claimed   []tracker.Issue `json:"claimed"`
	Blocked   []tracker.Issue `json:"blocked"`
	Epics     []tracker.Issue `json:"epics"`
	Untriaged int             `json:"untriaged"`
	Warnings  []warningOut    `json:"warnings"`
}

// handleBoard is the landing view: one snapshot sliced into the sections
// `hew list` orders. Untriaged issues appear only as a count, never as text;
// Triage is the one read that returns them.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	snap, err := s.tr.Snapshot(r.Context(), tracker.SnapshotOptions{})
	if err != nil {
		writeErr(w, err)
		return
	}
	untriaged := 0
	for _, i := range snap.Issues() {
		if i.Untriaged {
			untriaged++
		}
	}
	writeJSON(w, http.StatusOK, boardOut{
		Ready: lean(snap.Ready()), Claimed: lean(snap.Claimed()),
		Blocked: lean(snap.Blocked()), Epics: lean(snap.Epics()),
		Untriaged: untriaged, Warnings: warningsOut(snap.Warnings()),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	snap, err := s.tr.Snapshot(r.Context(), tracker.SnapshotOptions{})
	if err != nil {
		writeErr(w, err)
		return
	}
	ready := snap.Ready()
	if lim, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && lim > 0 && lim < len(ready) {
		ready = ready[:lim]
	}
	writeJSON(w, http.StatusOK, lean(ready))
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

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, err := states(q.Get("state"), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	filter := tracker.ListFilter{Label: q.Get("label")}
	if e := q.Get("epic"); e != "" {
		if filter.Epic, err = strconv.Atoi(e); err != nil || filter.Epic <= 0 {
			writeErr(w, &tracker.InvalidInputError{Field: "epic", Reason: "must be a positive issue number"})
			return
		}
	}
	snap, err := s.tr.Snapshot(r.Context(), tracker.SnapshotOptions{States: st})
	if err != nil {
		writeErr(w, err)
		return
	}
	issues := snap.List(filter)
	if lim, err := strconv.Atoi(q.Get("limit")); err == nil && lim > 0 && lim < len(issues) {
		issues = issues[:lim]
	}
	writeJSON(w, http.StatusOK, lean(issues))
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.tr.Detail(r.Context(), n)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailFrom(d))
}

func (s *Server) handleEpics(w http.ResponseWriter, r *http.Request) {
	st, err := states(r.URL.Query().Get("state"), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	snap, err := s.tr.Snapshot(r.Context(), tracker.SnapshotOptions{States: st})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lean(snap.Epics()))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, err := states(q.Get("state"), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.tr.Search(r.Context(), tracker.SearchRequest{Terms: strings.TrimSpace(q.Get("q")), States: st})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOut{Issues: lean(res.Issues), Total: res.Total, Fetched: res.Fetched})
}

type triageOut struct {
	listOut
	TextFindings []textFindingOut `json:"textFindings"`
}

// handleTriage is the one route that returns untriaged text. The UI says so
// before it calls it, as the CLI's primer does.
func (s *Server) handleTriage(w http.ResponseWriter, r *http.Request) {
	res, err := s.tr.Triage(r.Context(), tracker.TriageRequest{Search: strings.TrimSpace(r.URL.Query().Get("q"))})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, triageOut{
		listOut:      listOut{Issues: lean(res.Issues), Total: res.Total, Fetched: res.Fetched},
		TextFindings: textFindingsOut(res.TextFindings),
	})
}

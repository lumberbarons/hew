package local

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// The file is one JSON object per line. A record's _type picks its shape —
// the convention the beads snapshot already uses — and an issue record, the
// common case, carries none.
const (
	typeMeta  = "meta"
	typeLabel = "label"
	typeIssue = "issue"
)

type metaRec struct {
	Type    string `json:"_type"`
	Version int    `json:"version"`
}

type labelRec struct {
	Type        string `json:"_type"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
}

type commentRec struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	Body      string    `json:"body"`
}

// issueRec is the stored form of an issue. Relationships are stored once, on
// the issue that owns them (its parent, its blockers); everything the read
// path wants the other way round — sub-issues, ref states, rollups — is
// derived on load, so the file can never disagree with itself.
type issueRec struct {
	Type        string       `json:"_type,omitempty"`
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	Body        string       `json:"body,omitempty"`
	State       string       `json:"state"`
	StateReason string       `json:"state_reason,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	Labels      []string     `json:"labels,omitempty"`
	Assignees   []string     `json:"assignees,omitempty"`
	Parent      int          `json:"parent,omitempty"`
	BlockedBy   []int        `json:"blocked_by,omitempty"`
	Comments    []commentRec `json:"comments,omitempty"`
}

// data is the whole file in memory. Issues stay sorted by number.
type data struct {
	labels []gh.Label
	issues []*issueRec
}

func readData(path string) (*data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return decodeData(f, path)
}

// decodeData parses the file's records; path only names the source in errors.
func decodeData(r io.Reader, path string) (*data, error) {
	d := &data{}
	seen := map[int]bool{}
	sawMeta := false
	dec := json.NewDecoder(r)
	for n := 1; ; n++ {
		var raw json.RawMessage
		if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%s: record %d: %w", path, n, err)
		}
		bad := func(err error) error { return fmt.Errorf("%s: record %d: %w", path, n, err) }

		var head struct {
			Type string `json:"_type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, bad(err)
		}
		switch head.Type {
		case typeMeta:
			var m metaRec
			if err := strictUnmarshal(raw, &m); err != nil {
				return nil, bad(err)
			}
			if m.Version > formatVersion {
				return nil, bad(fmt.Errorf("written by a newer hew (format %d, this build reads %d)", m.Version, formatVersion))
			}
			sawMeta = true
		case typeLabel:
			var l labelRec
			if err := strictUnmarshal(raw, &l); err != nil {
				return nil, bad(err)
			}
			d.labels = append(d.labels, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description})
		case "", typeIssue:
			var rec issueRec
			if err := strictUnmarshal(raw, &rec); err != nil {
				return nil, bad(err)
			}
			if rec.Number <= 0 || seen[rec.Number] {
				return nil, bad(fmt.Errorf("issue number %d is invalid or repeated", rec.Number))
			}
			if rec.State != string(gh.StateOpen) && rec.State != string(gh.StateClosed) {
				return nil, bad(fmt.Errorf("issue %d has unknown state %q (want %s or %s)", rec.Number, rec.State, gh.StateOpen, gh.StateClosed))
			}
			seen[rec.Number] = true
			rec.Type = ""
			d.issues = append(d.issues, &rec)
		default:
			return nil, bad(fmt.Errorf("unknown record type %q", head.Type))
		}
	}
	if !sawMeta {
		return nil, fmt.Errorf("%s: no meta record; not a hew issues file", path)
	}
	slices.SortFunc(d.issues, func(a, b *issueRec) int { return a.Number - b.Number })
	return d, nil
}

func (d *data) encode() ([]byte, error) {
	var buf bytes.Buffer
	write := func(v any) error {
		line, err := marshalLine(v)
		if err != nil {
			return err
		}
		buf.Write(line)
		return nil
	}
	if err := write(metaRec{Type: typeMeta, Version: formatVersion}); err != nil {
		return nil, err
	}
	for _, l := range d.labels {
		if err := write(labelRec{Type: typeLabel, Name: l.Name, Color: l.Color, Description: l.Description}); err != nil {
			return nil, err
		}
	}
	for _, rec := range d.issues {
		if err := write(rec); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// marshalLine encodes v as one line. HTML escaping is off because bodies
// carry <!-- --> provenance markers that should stay readable in the file.
func marshalLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// strictUnmarshal refuses unknown fields: the file is rewritten on every
// mutation, so a field this build doesn't know would be silently dropped.
func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (d *data) find(number int) (*issueRec, error) {
	for _, rec := range d.issues {
		if rec.Number == number {
			return rec, nil
		}
	}
	return nil, fmt.Errorf("issue #%d not found", number)
}

// build derives the read-side view of every issue. Nested connections are
// never capped here, so each Total equals the length of its list.
func (d *data) build() []model.Issue {
	byNumber := make(map[int]*issueRec, len(d.issues))
	children := map[int][]model.Ref{}
	for _, rec := range d.issues {
		byNumber[rec.Number] = rec
		if rec.Parent != 0 {
			children[rec.Parent] = append(children[rec.Parent], model.Ref{Number: rec.Number, State: rec.State})
		}
	}
	out := make([]model.Issue, 0, len(d.issues))
	for _, rec := range d.issues {
		i := model.Issue{
			Number: rec.Number, Title: rec.Title, Body: rec.Body,
			State: rec.State, StateReason: rec.StateReason, CreatedAt: rec.CreatedAt,
			Labels: slices.Clone(rec.Labels), Assignees: slices.Clone(rec.Assignees),
		}
		if p := byNumber[rec.Parent]; p != nil {
			i.Parent = &model.Ref{Number: p.Number, State: p.State}
			i.ParentTitle = p.Title
		}
		i.SubIssues = children[rec.Number]
		i.SubIssuesTotal = len(i.SubIssues)
		for _, c := range i.SubIssues {
			if !c.IsOpen() {
				i.SubIssuesCompleted++
			}
		}
		for _, n := range rec.BlockedBy {
			if b := byNumber[n]; b != nil {
				i.BlockedBy = append(i.BlockedBy, model.Ref{Number: b.Number, State: b.State})
			}
		}
		i.BlockedByTotal = len(i.BlockedBy)
		out = append(out, i)
	}
	return out
}

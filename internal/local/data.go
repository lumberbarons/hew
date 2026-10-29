package local

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
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

// extras holds the fields of a record this build does not know. A newer hew
// may add fields without bumping the format version, and every mutation
// rewrites the whole file, so they are carried through verbatim rather than
// dropped or refused.
type extras map[string]json.RawMessage

type metaRec struct {
	Type    string `json:"_type"`
	Version int    `json:"version"`
	extra   extras
}

type labelRec struct {
	Type        string `json:"_type"`
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
	extra       extras
}

type commentRec struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	Body      string    `json:"body"`
	extra     extras
}

// UnmarshalJSON keeps a comment's unknown fields, which the generic decode of
// the enclosing issue would otherwise drop.
func (r *commentRec) UnmarshalJSON(raw []byte) error {
	type plain commentRec
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	x, err := unknownFields(raw, p)
	if err != nil {
		return err
	}
	p.extra = x
	*r = commentRec(p)
	return nil
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
	extra       extras
}

// The marshalers append a record's unknown fields after the ones it knows.
// The plain aliases drop the methods, so marshalling them does not recurse.

func (r commentRec) MarshalJSON() ([]byte, error) {
	type plain commentRec
	return marshalWithExtras(plain(r), r.extra)
}

func (r metaRec) MarshalJSON() ([]byte, error) {
	type plain metaRec
	return marshalWithExtras(plain(r), r.extra)
}

func (r labelRec) MarshalJSON() ([]byte, error) {
	type plain labelRec
	return marshalWithExtras(plain(r), r.extra)
}

func (r issueRec) MarshalJSON() ([]byte, error) {
	type plain issueRec
	return marshalWithExtras(plain(r), r.extra)
}

// marshalWithExtras encodes v, a struct that always has at least one field
// set, and splices x in before the closing brace in a stable order. Known
// fields win: x never holds a name v already writes.
func marshalWithExtras(v any, x extras) ([]byte, error) {
	line, err := marshalLine(v)
	if err != nil {
		return nil, err
	}
	out := bytes.TrimSuffix(bytes.TrimRight(line, "\n"), []byte("}"))
	sep := byte(',')
	if bytes.Equal(out, []byte("{")) {
		sep = 0 // nothing written yet: no comma before the first field
	}
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		name, err := marshalLine(k)
		if err != nil {
			return nil, err
		}
		if sep != 0 {
			out = append(out, sep)
		}
		sep = ','
		out = append(out, bytes.TrimRight(name, "\n")...)
		out = append(out, ':')
		out = append(out, x[k]...)
	}
	return append(out, '}'), nil
}

// mergeExtras folds src into dst, later values winning. A hand-edited or badly
// merged file can repeat a meta or label record; neither copy's fields are lost.
func mergeExtras(dst, src extras) extras {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = extras{}
	}
	maps.Copy(dst, src)
	return dst
}

// unknownFields returns the fields of raw that v's type has no json name for.
// Names are compared case-insensitively because decoding matches them that
// way: "Title" fills Title, so it is not unknown.
func unknownFields(raw []byte, v any) (extras, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	t := reflect.TypeOf(v)
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		for k := range all {
			if strings.EqualFold(k, tag) {
				delete(all, k)
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// data is the whole file in memory. Issues stay sorted by number.
type data struct {
	labels []gh.Label
	issues []*issueRec

	// Unknown fields of the meta record and of each label, by label name.
	metaExtra  extras
	labelExtra map[string]extras
}

func readData(path string) (*data, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, missingIssuesError(path)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return decodeData(f, path)
}

// decodeData parses the file's records, one per line; path only names the
// source in errors. Blank lines are skipped but still counted, so a reported
// line number is the one an editor shows.
func decodeData(r io.Reader, path string) (*data, error) {
	d := &data{}
	seen := map[int]bool{}
	sawMeta := false
	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		line, readErr := br.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("%s: line %d: %w", path, n, readErr)
		}
		if raw := bytes.TrimSpace(line); len(raw) > 0 {
			meta, err := d.decodeRecord(raw, seen)
			if err != nil {
				var newer tooNewError
				if errors.As(err, &newer) {
					// Not damaged, only from a newer hew: repairing it would corrupt it.
					return nil, fmt.Errorf("%s: line %d: %w", path, n, err)
				}
				return nil, fmt.Errorf("%s: line %d: %w (to recover, fix or remove that line, or restore the file from a backup)", path, n, err)
			}
			sawMeta = sawMeta || meta
		}
		if readErr != nil {
			break
		}
	}
	if !sawMeta {
		return nil, fmt.Errorf("%s: no meta record; not a hew issues file", path)
	}
	slices.SortFunc(d.issues, func(a, b *issueRec) int { return a.Number - b.Number })
	return d, nil
}

// tooNewError marks a record this build cannot read because a newer hew wrote
// it, as opposed to one that is damaged.
type tooNewError struct{ msg string }

func (e tooNewError) Error() string { return e.msg }

// decodeRecord adds one record to d and reports whether it was the meta record.
func (d *data) decodeRecord(raw []byte, seen map[int]bool) (isMeta bool, err error) {
	var head struct {
		Type string `json:"_type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return false, err
	}
	switch head.Type {
	case typeMeta:
		var m metaRec
		if err := json.Unmarshal(raw, &m); err != nil {
			return false, err
		}
		if m.Version > formatVersion {
			return false, tooNewError{fmt.Sprintf("written by a newer hew (format %d, this build reads %d)", m.Version, formatVersion)}
		}
		x, err := unknownFields(raw, m)
		if err != nil {
			return false, err
		}
		d.metaExtra = mergeExtras(d.metaExtra, x)
		return true, nil
	case typeLabel:
		var l labelRec
		if err := json.Unmarshal(raw, &l); err != nil {
			return false, err
		}
		x, err := unknownFields(raw, l)
		if err != nil {
			return false, err
		}
		if x != nil {
			if d.labelExtra == nil {
				d.labelExtra = map[string]extras{}
			}
			d.labelExtra[l.Name] = mergeExtras(d.labelExtra[l.Name], x)
		}
		d.labels = append(d.labels, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	case "", typeIssue:
		var rec issueRec
		if err := json.Unmarshal(raw, &rec); err != nil {
			return false, err
		}
		if rec.Number <= 0 || seen[rec.Number] {
			return false, fmt.Errorf("issue number %d is invalid or repeated", rec.Number)
		}
		if rec.State != string(gh.StateOpen) && rec.State != string(gh.StateClosed) {
			return false, fmt.Errorf("issue %d has unknown state %q (want %s or %s)", rec.Number, rec.State, gh.StateOpen, gh.StateClosed)
		}
		if rec.extra, err = unknownFields(raw, rec); err != nil {
			return false, err
		}
		seen[rec.Number] = true
		rec.Type = ""
		d.issues = append(d.issues, &rec)
	default:
		return false, tooNewError{fmt.Sprintf("unknown record type %q (written by a newer hew?)", head.Type)}
	}
	return false, nil
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
	if err := write(metaRec{Type: typeMeta, Version: formatVersion, extra: d.metaExtra}); err != nil {
		return nil, err
	}
	for _, l := range d.labels {
		if err := write(labelRec{Type: typeLabel, Name: l.Name, Color: l.Color, Description: l.Description, extra: d.labelExtra[l.Name]}); err != nil {
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

// strictUnmarshal refuses unknown fields. It guards the small files that are
// rewritten from scratch (the migration record); config.json and the issue file
// carry unknown fields through instead.
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

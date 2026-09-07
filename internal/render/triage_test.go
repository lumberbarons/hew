package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func triageFixtures() []model.Issue {
	return []model.Issue{
		{Number: 1, Title: "Clean 👩‍💻 report", State: "OPEN", CreatedAt: ts(1)},
		{Number: 12, Title: "Suspicious report", State: "CLOSED", CreatedAt: ts(2),
			Body: "\u200b\u202e\U000e0069\u0430", BlockedBy: []model.Ref{{Number: 3, State: "OPEN"}}},
	}
}

func TestTriageRendering(t *testing.T) {
	var buf bytes.Buffer
	Triage(&buf, triageFixtures(), Style{})
	checkGolden(t, "triage", buf.Bytes())
}

func TestJSONTriage(t *testing.T) {
	var buf bytes.Buffer
	if err := JSONTriage(&buf, triageFixtures()); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "triage_json", buf.Bytes())
}

func TestTriageRetainsTitleAndSanitizesControls(t *testing.T) {
	i := hostileIssueWith(hostileValidUTF8 + "\u200b\u0430")
	var buf bytes.Buffer
	Triage(&buf, []model.Issue{i}, Style{})
	assertNeutralized(t, "Triage", buf.String())
	if !strings.Contains(buf.String(), SanitizeInline(i.Title)) ||
		!strings.Contains(buf.String(), "[contains zero-width characters; contains confusable characters]") {
		t.Errorf("missing title or findings: %q", buf.String())
	}
	buf.Reset()
	if err := JSONTriage(&buf, []model.Issue{i}); err != nil {
		t.Fatal(err)
	}
	var got IssueJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != i.Title {
		t.Errorf("JSON title changed: %q", got.Title)
	}
}

type triageErrorWriter struct{ err error }

func (w triageErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestJSONTriageWriterError(t *testing.T) {
	want := errors.New("write failed")
	if got := JSONTriage(triageErrorWriter{want}, triageFixtures()); !errors.Is(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTriageEmptyJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSONTriage(&buf, nil); err != nil || buf.Len() != 0 {
		t.Errorf("empty NDJSON = %q, error = %v", buf.String(), err)
	}
}

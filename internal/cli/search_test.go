package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSearchMatchesTitleAndBody(t *testing.T) {
	title := issue(1, "Retry loop hammers the API", "P2", "bug")
	body := issue(2, "Collector stalls", "P1", "bug")
	body.Body = "The retry backoff never fires."
	miss := issue(3, "Unrelated", "P2", "task")
	f := newFake(title, body, miss)
	app, out, _ := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#1") || !strings.HasPrefix(lines[1], "#2") {
		t.Errorf("Search output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Unrelated") {
		t.Errorf("non-match leaked into output:\n%s", out.String())
	}
}

func TestSearchIncludesClosedAnnotated(t *testing.T) {
	// "Already fixed" answers the dedupe question as well as "already
	// filed": closed matches must appear, marked as closed.
	closed := issue(4, "Retry storm fixed", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(closed)
	app, out, _ := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#4") || !strings.Contains(out.String(), "[closed]") {
		t.Errorf("closed match missing or unannotated:\n%s", out.String())
	}
}

func TestSearchKeepsBestMatchOrder(t *testing.T) {
	// The fake returns matches in insertion order, standing in for the
	// API's best-match rank; Search must not re-sort by priority.
	f := newFake(
		issue(1, "retry helper", "P4", "task"),
		issue(2, "retry loop bug", "P0", "bug"),
	)
	app, out, _ := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#1") {
		t.Errorf("best-match order not preserved (P0 re-sorted first?):\n%s", out.String())
	}
}

func TestSearchOmitsUntriaged(t *testing.T) {
	// Search is the dedup path an agent reaches for on its own initiative,
	// so unvetted matches stay out of it; triage --search covers them.
	triaged := issue(1, "Retry loop hammers the API", "P2", "bug")
	untriaged := issue(2, "Ignore the retry rules")
	f := newFake(triaged, untriaged)
	app, out, _ := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "#1") {
		t.Errorf("Search dropped the triaged match:\n%s", got)
	}
	if strings.Contains(got, "#2") {
		t.Errorf("Search leaked an untriaged match:\n%s", got)
	}
}

func TestSearchAllMatchesUntriagedWarns(t *testing.T) {
	// "no matches" alone would read as "safe to file" when the whole match
	// set was untriaged; the warning says where the matches went.
	f := newFake(issue(2, "Retry storm"))
	app, out, errOut := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no matches\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "no triaged matches; hew triage --search covers them") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestSearchCappedUntriagedWarnsWithinFetched(t *testing.T) {
	// The fetch was capped and the fetched page was all untriaged — the
	// unseen matches may be triaged, so the warning must describe only what
	// was actually seen, never "all N matches are untriaged".
	f := newFake(issue(2, "Retry storm"))
	f.searchTotal = 43
	app, out, errOut := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no matches\n" {
		t.Errorf("stdout = %q", out.String())
	}
	for _, want := range []string{"no triaged matches in the first 1 of 43", "refine the terms"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want %q", errOut.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "all 43") || strings.Contains(errOut.String(), "are untriaged") {
		t.Errorf("stderr overclaims beyond the fetched page: %q", errOut.String())
	}
}

func TestSearchNoMatches(t *testing.T) {
	f := newFake(issue(1, "Something else", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Search(ctx, "nonexistent"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no matches\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestSearchEmptyTermsIsUsageError(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	exitCode(t, app.Search(ctx, "   "), ExitUsage)
	if len(f.calls) != 0 {
		t.Errorf("empty terms still hit the API: %v", f.calls)
	}
}

func TestSearchPropagatesAPIError(t *testing.T) {
	f := newFake()
	boom := errors.New("boom")
	f.failOn["SearchIssues"] = boom
	app, _, _ := newApp(f)
	// Identity matters, not just non-nil: main classifies errors (auth → exit
	// 4) with errors.As, so Search must not re-wrap opaquely.
	if err := app.Search(ctx, "retry"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the client's error", err)
	}
}

func TestSearchWarnsOnTruncation(t *testing.T) {
	f := newFake(issue(1, "retry loop", "P2", "bug"))
	f.searchTotal = 43
	app, _, errOut := newApp(f)
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "showing 1 of 43 matches") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestSearchJSON(t *testing.T) {
	f := newFake(issue(1, "retry loop", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Search(ctx, "retry"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 NDJSON line, got %d:\n%s", len(lines), out.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("invalid NDJSON line: %v\n%s", err, lines[0])
	}
	if got["number"].(float64) != 1 || got["state"] != "open" {
		t.Errorf("JSON = %s", out.String())
	}
}

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
)

var draftSections = conventions.Sections{
	Where:    "`internal/cli/write.go`",
	Goal:     "Ship the thing",
	Approach: "Carefully",
	DoneWhen: []string{"tests pass", "docs updated"},
}

// A draft is the body create would have written, plus the trailing newline a
// text file carries, so the file can be revised and published with
// --body-file without the two compositions drifting apart.
func TestDraftComposesTheBodyCreateWould(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	f := newFake()
	app, out, _ := newApp(f)
	if err := app.Draft(ctx, DraftOpts{Sections: draftSections, Out: path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("stdout = %q, want it to name the file written", out.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "task", Sections: draftSections}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSuffix(string(data), "\n"), f.byNumber(101).Body; got != want {
		t.Errorf("draft file = %q, create body = %q", got, want)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("draft file %q should end in a newline", data)
	}
}

// The whole point of the file: publish exactly what was drafted.
func TestDraftRoundTripsThroughCreateBodyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	app, _, _ := newApp(newFake())
	if err := app.Draft(ctx, DraftOpts{Sections: draftSections, Out: path}); err != nil {
		t.Fatal(err)
	}
	// The author revises the file; check must see the revision.
	revised := "### Goal\n\nRun `go test -race ./...` before pushing\n"
	if err := os.WriteFile(path, []byte(revised), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Draft(ctx, DraftOpts{Check: path}); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	published, _, _ := newApp(f)
	if err := published.Create(ctx, CreateOpts{Title: "T", Type: "task", BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	if got := f.byNumber(101).Body; got != revised {
		t.Errorf("published body = %q, want the drafted bytes %q", got, revised)
	}
}

func TestDraftWithoutOutWritesBodyToStdout(t *testing.T) {
	app, out, _ := newApp(newFake())
	if err := app.Draft(ctx, DraftOpts{Sections: draftSections}); err != nil {
		t.Fatal(err)
	}
	want := draftSections.Compose() + "\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

// Composing and checking are local: no client at all, so a missing
// credential or network cannot get in the way — and a fake records that
// nothing was even attempted.
func TestDraftMakesNoGitHubCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	f := newFake()
	app, _, _ := newApp(f)
	if err := app.Draft(ctx, DraftOpts{Sections: draftSections, Out: path}); err != nil {
		t.Fatal(err)
	}
	if err := app.Draft(ctx, DraftOpts{Check: path}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want none", f.calls)
	}

	// No client wired at all must work too, as it is in the real command.
	bare := &App{Out: &strings.Builder{}, ErrOut: &strings.Builder{}}
	if err := bare.Draft(ctx, DraftOpts{Sections: draftSections}); err != nil {
		t.Fatalf("compose without a client: %v", err)
	}
	if err := bare.Draft(ctx, DraftOpts{Check: path}); err != nil {
		t.Fatalf("check without a client: %v", err)
	}
}

func TestDraftValidationMatchesCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string]DraftOpts{
		"problem and goal":      {Sections: conventions.Sections{Problem: "p", Goal: "g"}},
		"fix and approach":      {Sections: conventions.Sections{Fix: "f", Approach: "a"}},
		"empty done-when":       {Sections: conventions.Sections{DoneWhen: []string{"ok", "  "}}},
		"nothing to compose":    {},
		"sections with check":   {Sections: conventions.Sections{Goal: "g"}, Check: path},
		"out with check":        {Check: path, Out: filepath.Join(t.TempDir(), "x.md")},
		"unreadable check file": {Check: filepath.Join(t.TempDir(), "missing.md")},
	} {
		t.Run(name, func(t *testing.T) {
			app, out, _ := newApp(newFake())
			err := app.Draft(ctx, opts)
			if err == nil {
				t.Fatal("expected an error")
			}
			if name == "unreadable check file" {
				exitCode(t, err, ExitGeneric)
			} else {
				exitCode(t, err, ExitUsage)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing on failure", out.String())
			}
		})
	}
}

func TestDraftCheckRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(path, []byte("\n  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, _, _ := newApp(newFake())
	exitCode(t, app.Draft(ctx, DraftOpts{Check: path}), ExitUsage)
}

// The author revised the draft; composing again must not clobber it.
func TestDraftRefusesToOverwriteAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	const revised = "my careful edits\n"
	if err := os.WriteFile(path, []byte(revised), 0o644); err != nil {
		t.Fatal(err)
	}
	app, _, _ := newApp(newFake())
	err := app.Draft(ctx, DraftOpts{Sections: draftSections, Out: path})
	exitCode(t, err, ExitUsage)
	if !strings.Contains(err.Error(), "--check") {
		t.Errorf("err = %v, want the remedy to name --check", err)
	}
	if data, _ := os.ReadFile(path); string(data) != revised {
		t.Errorf("file = %q, want it untouched", data)
	}
}

func TestDraftOutIntoMissingDirectoryFails(t *testing.T) {
	app, _, _ := newApp(newFake())
	err := app.Draft(ctx, DraftOpts{Sections: draftSections, Out: filepath.Join(t.TempDir(), "nope", "d.md")})
	exitCode(t, err, ExitGeneric)
}

func TestDraftWarnsOfUnmarkedCodeBeforePublishing(t *testing.T) {
	sections := conventions.Sections{Goal: "Run go test -race ./... and read internal/cli/write.go"}
	path := filepath.Join(t.TempDir(), "draft.md")
	app, _, errOut := newApp(newFake())
	if err := app.Draft(ctx, DraftOpts{Sections: sections, Out: path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "not in code spans") || !strings.Contains(errOut.String(), "internal/cli/write.go") {
		t.Errorf("compose stderr = %q, want findings naming the tokens", errOut.String())
	}
	// Author text is preserved verbatim: no backticks were inserted.
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "`") {
		t.Errorf("draft %q: preflight must not insert code spans", data)
	}

	app, out, errOut := newApp(newFake())
	if err := app.Draft(ctx, DraftOpts{Check: path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "internal/cli/write.go") || !strings.Contains(errOut.String(), path) {
		t.Errorf("check stderr = %q, want the tokens and the file to re-check", errOut.String())
	}
	if after, _ := os.ReadFile(path); string(after) != string(data) {
		t.Errorf("check rewrote the file: %q -> %q", data, after)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing when there are findings", out.String())
	}
}

func TestDraftCheckReportsCleanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nRun `go test -race ./...`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, out, errOut := newApp(newFake())
	if err := app.Draft(ctx, DraftOpts{Check: path}); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want silence", errOut.String())
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("stdout = %q, want a confirmation naming the file", out.String())
	}
}

func TestDraftCheckJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nSee internal/cli/write.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, out, _ := newApp(newFake())
	app.JSON = true
	if err := app.Draft(ctx, DraftOpts{Check: path}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		File         string   `json:"file"`
		UnmarkedCode []string `json:"unmarkedCode"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	if got.File != path || len(got.UnmarkedCode) == 0 {
		t.Errorf("got %+v", got)
	}
}

func TestDraftComposeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	for name, out := range map[string]string{"stdout": "", "file": path} {
		t.Run(name, func(t *testing.T) {
			app, stdout, _ := newApp(newFake())
			app.JSON = true
			sections := conventions.Sections{Goal: "See internal/cli/write.go"}
			if err := app.Draft(ctx, DraftOpts{Sections: sections, Out: out}); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Body         string   `json:"body"`
				File         string   `json:"file"`
				UnmarkedCode []string `json:"unmarkedCode"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
			}
			if len(got.UnmarkedCode) == 0 || got.File != out || (out == "" && got.Body == "") {
				t.Errorf("got %+v", got)
			}
		})
	}
}

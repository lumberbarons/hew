package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// childlessEpicBackends runs a test body against the fake client and the
// local store: a childless epic has to behave the same on both, since the
// local store derives sub-issues on load and the fake keeps them as given.
func childlessEpicBackends(t *testing.T, body func(t *testing.T, app *App, out *bytes.Buffer)) {
	t.Helper()
	t.Run("fake", func(t *testing.T) {
		app, out, _ := newApp(newFake())
		body(t, app, out)
	})
	t.Run("local", func(t *testing.T) {
		store, _ := localTracker(t)
		app, out, _ := newLocalApp(store)
		body(t, app, out)
	})
}

func onlyOpenIssue(t *testing.T, app *App) model.Issue {
	t.Helper()
	issues, err := app.Client.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
	mustLocal(t, err)
	if len(issues) != 1 {
		t.Fatalf("open issues = %d, want 1", len(issues))
	}
	return issues[0]
}

func TestEpicCreateChildlessIsVisibleAndNotWorkable(t *testing.T) {
	childlessEpicBackends(t, func(t *testing.T, app *App, out *bytes.Buffer) {
		mustLocal(t, app.EpicCreate(ctx, EpicCreateOpts{Title: "Roadmap"}))
		n := onlyOpenIssue(t, app).Number

		out.Reset()
		mustLocal(t, app.List(ctx, ListOpts{}))
		if !strings.Contains(out.String(), "Epic: Roadmap") || !strings.Contains(out.String(), "epic 0/0") {
			t.Errorf("list omits or misannotates the childless epic:\n%s", out.String())
		}

		out.Reset()
		mustLocal(t, app.EpicStatus(ctx, 0))
		if !strings.Contains(out.String(), "Epic: Roadmap") {
			t.Errorf("epic status omits the childless epic:\n%s", out.String())
		}

		out.Reset()
		mustLocal(t, app.EpicStatus(ctx, n))
		if !strings.Contains(out.String(), "0/0") {
			t.Errorf("epic status <n> refuses the childless epic:\n%s", out.String())
		}

		out.Reset()
		mustLocal(t, app.Search(ctx, "Roadmap"))
		if !strings.Contains(out.String(), "Epic: Roadmap") {
			t.Errorf("search omits the childless epic:\n%s", out.String())
		}

		out.Reset()
		mustLocal(t, app.Ready(ctx, ReadyOpts{}))
		if strings.Contains(out.String(), "Roadmap") {
			t.Errorf("ready offers the childless epic:\n%s", out.String())
		}

		err := app.Start(ctx, n, StartOpts{})
		exitCode(t, err, ExitGeneric)
		if err == nil || !strings.Contains(err.Error(), "is an epic") {
			t.Errorf("start error = %v, want the epic refusal", err)
		}
	})
}

func TestEpicCreateChildlessIsTriagedWithoutAnArea(t *testing.T) {
	childlessEpicBackends(t, func(t *testing.T, app *App, _ *bytes.Buffer) {
		mustLocal(t, app.EpicCreate(ctx, EpicCreateOpts{Title: "Roadmap"}))
		got := onlyOpenIssue(t, app)
		if got.Untriaged() || !got.IsEpic() {
			t.Errorf("issue labels %v: want a triaged epic", got.Labels)
		}
		if areas := got.Areas(); len(areas) != 0 {
			t.Errorf("areas = %v; the epic marker must not read as an area", areas)
		}
	})
}

func TestEpicLabelIsReservedFromAreas(t *testing.T) {
	if !model.IsConventionLabel("Epic") {
		t.Error("the epic marker is a convention label in any case")
	}
	err := validateAreaLabels("--area", []string{"epic"})
	exitCode(t, err, ExitUsage)
}

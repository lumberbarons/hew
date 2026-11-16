package pr

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

func TestCreateDraftsForTheClaimedIssue(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "PR workflow", "P2", "task"))

	result, err := client.Create(t.Context(), CreateRequest{
		Body: Body{Sections: sections("", "", "go test")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := CreateResult{
		Number: 501, URL: "https://github.com/o/r/pull/501", Draft: true,
		Fixes: 30, FixedIssues: []int{30},
		Head: "feat/x", Base: "main", Title: "chore: PR workflow",
	}
	if result.Number != want.Number || result.URL != want.URL || result.Draft != want.Draft ||
		result.Fixes != want.Fixes || !slices.Equal(result.FixedIssues, want.FixedIssues) ||
		result.Head != want.Head || result.Base != want.Base || result.Title != want.Title {
		t.Errorf("result = %+v, want %+v", result, want)
	}
	if len(prs.created) != 1 {
		t.Fatalf("created %d pull requests, want one", len(prs.created))
	}
	created := prs.created[0]
	if created.Head != "feat/x" || created.Base != "main" || !created.Draft {
		t.Errorf("created = %+v", created)
	}
	if !strings.HasSuffix(created.Body, "Fixes #30") {
		t.Errorf("body does not close the issue:\n%s", created.Body)
	}
	// Testing is supplied; Risk is a draft's to omit.
	if len(result.Warnings) != 0 {
		t.Errorf("warnings = %+v", result.Warnings)
	}
}

// The claim is the primary signal, not the branch name: a lone claim whose
// branch names nothing is still the issue the pull request closes.
func TestCreateInfersTheClaim(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "the claim"))
	if _, err := client.Create(t.Context(), CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	if body := prs.created[0].Body; !strings.HasSuffix(body, "Fixes #30") {
		t.Errorf("body = %q", body)
	}
}

// GitHub knows the branch by its upstream name only, so a pull request opened
// from the worktree's local name is rejected. Both the create and the
// existing-pull-request lookup go out under the resolved name.
func TestCreateResolvesTheUpstreamHead(t *testing.T) {
	store := &fakeStore{issues: []model.Issue{claimed(30, "one")}, viewer: "me"}
	prs := &fakePRs{defaultBranch: "main"}
	client := Wrap(store, prs, gh.Repo{Owner: "o", Name: "r"},
		git.State{Branch: "worktree-fix+pr-head", Upstream: "fix/pr-head", Pushed: true})

	if _, err := client.Create(t.Context(), CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prs.contexts, []string{"fix/pr-head"}) {
		t.Errorf("existing-PR lookups = %v", prs.contexts)
	}
	if got := prs.created[0].Head; got != "fix/pr-head" {
		t.Errorf("head = %q, want the upstream branch", got)
	}
}

// The push advice is the one place the local name is the right answer: it is
// what the user has checked out and what they would type.
func TestCreateRefusesAnUnpushedBranch(t *testing.T) {
	store := &fakeStore{issues: []model.Issue{claimed(30, "one")}, viewer: "me"}
	prs := &fakePRs{defaultBranch: "main"}
	client := Wrap(store, prs, gh.Repo{Owner: "o", Name: "r"},
		git.State{Branch: "worktree-feat+x", Pushed: false})

	_, err := client.Create(t.Context(), CreateRequest{})
	var noUpstream *NoUpstreamError
	if !errors.As(err, &noUpstream) {
		t.Fatalf("err = %T (%v), want *NoUpstreamError", err, err)
	}
	if noUpstream.Branch != "worktree-feat+x" {
		t.Errorf("Branch = %q, want the local name", noUpstream.Branch)
	}
	if !strings.Contains(err.Error(), "git push -u origin worktree-feat+x") {
		t.Errorf("err = %q, want the push advice", err)
	}
	if len(prs.contexts) != 0 || len(prs.created) != 0 {
		t.Errorf("the API was called before the branch check: %v", prs.contexts)
	}
}

func TestCreateRefusesAnExistingPullRequest(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "one"))
	prs.existing = &gh.PullRequest{Number: 501}
	_, err := client.Create(t.Context(), CreateRequest{})
	var existing *ExistingPullRequestError
	if !errors.As(err, &existing) {
		t.Fatalf("err = %T (%v), want *ExistingPullRequestError", err, err)
	}
	if existing.Number != 501 || existing.Issue != 30 || existing.Head != "feat/x" {
		t.Errorf("error = %+v", existing)
	}
	if len(prs.created) != 0 {
		t.Error("a pull request was created despite the refusal")
	}
}

func TestCreateBase(t *testing.T) {
	t.Run("overrides the default", func(t *testing.T) {
		client, _, prs := wrapped(claimed(30, "one"))
		if _, err := client.Create(t.Context(), CreateRequest{Base: "release/1.x"}); err != nil {
			t.Fatal(err)
		}
		if got := prs.created[0].Base; got != "release/1.x" {
			t.Errorf("base = %q", got)
		}
	})
	t.Run("missing default branch is a typed refusal", func(t *testing.T) {
		client, _, prs := wrapped(claimed(30, "one"))
		prs.defaultBranch = ""
		_, err := client.Create(t.Context(), CreateRequest{})
		assertInvalid(t, err, FieldBase)
		if len(prs.created) != 0 {
			t.Error("a pull request was created without a base")
		}
	})
	t.Run("the default branch itself is not a head", func(t *testing.T) {
		client, _, prs := wrapped(claimed(30, "one"))
		client.state = git.State{Branch: "main", Pushed: true}
		_, err := client.Create(t.Context(), CreateRequest{})
		assertInvalid(t, err, FieldHead)
		if len(prs.created) != 0 {
			t.Error("a pull request was opened from the base branch")
		}
	})
}

func assertInvalid(t *testing.T, err error, field string) {
	t.Helper()
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T (%v), want *tracker.InvalidInputError", err, err)
	}
	if invalid.Field != field {
		t.Errorf("field = %q, want %q (%v)", invalid.Field, field, err)
	}
}

func TestCreateWarnsOnANonConventionalBranch(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "one"))
	client.state = git.State{Branch: "wip", Pushed: true}
	result, err := client.Create(t.Context(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var prefix *Warning
	for i, w := range result.Warnings {
		if w.Kind == WarnBranchPrefix {
			prefix = &result.Warnings[i]
		}
	}
	if prefix == nil || prefix.Branch != "wip" {
		t.Fatalf("warnings = %+v, want the branch-prefix warning", result.Warnings)
	}
	// The warning is advice, not a refusal — the work is already committed.
	if len(prs.created) != 1 {
		t.Error("the branch-prefix warning refused the create")
	}
}

func TestCreateWarnsAboutClaimsAndUnrelatedSets(t *testing.T) {
	other := claimed(50, "second", "cli")
	other.Assignees = []string{"someone"}
	first := claimed(46, "first", "cli")
	unrelated := fixture(51, "third", "infra")
	client, _, _ := wrapped(first, other, unrelated)
	result, err := client.Create(t.Context(), CreateRequest{
		For: []int{46, 50, 51}, Body: Body{Sections: sections("w", "y", "t")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []WarningKind
	for _, w := range result.Warnings {
		kinds = append(kinds, w.Kind)
	}
	want := []WarningKind{WarnUnclaimed, WarnUnclaimed, WarnUnrelatedSet, WarnPrimaryTitle}
	if !slices.Equal(kinds, want) {
		t.Fatalf("warnings = %+v, want kinds %v", result.Warnings, want)
	}
	foreign := result.Warnings[0]
	if foreign.Issue != 50 || !slices.Equal(foreign.Assignees, []string{"someone"}) || foreign.Actor != "me" {
		t.Errorf("foreign claim warning = %+v", foreign)
	}
	if unclaimed := result.Warnings[1]; unclaimed.Issue != 51 || len(unclaimed.Assignees) != 0 {
		t.Errorf("unclaimed warning = %+v", unclaimed)
	}
	if groups := result.Warnings[2].Groups; len(groups) != 2 {
		t.Errorf("groups = %v, want the split set", groups)
	}
}

func TestCreateDoesNotWarnAboutARelatedSet(t *testing.T) {
	client, _, _ := wrapped(claimed(46, "first", "cli"), claimed(50, "second", "cli"))
	result, err := client.Create(t.Context(), CreateRequest{
		For: []int{46, 50}, Body: Body{Sections: sections("w", "y", "t")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range result.Warnings {
		if w.Kind == WarnUnrelatedSet {
			t.Errorf("warned about a related set: %+v", w)
		}
	}
}

func TestCreateTitleConventions(t *testing.T) {
	for name, tc := range map[string]struct {
		labels []string
		title  string
		want   string
	}{
		"enhancement": {[]string{"P2", "enhancement"}, "", "feat: the work"},
		"bug":         {[]string{"P2", "bug"}, "", "fix: the work"},
		"task":        {[]string{"P2", "task"}, "", "chore: the work"},
		"untyped":     {[]string{"P2"}, "", "the work"},
		"explicit":    {[]string{"P2", "enhancement"}, "hand-written, unprefixed", "hand-written, unprefixed"},
	} {
		t.Run(name, func(t *testing.T) {
			client, _, prs := wrapped(claimed(30, "the work", tc.labels...))
			result, err := client.Create(t.Context(), CreateRequest{
				Title: tc.title, Body: Body{Sections: sections("w", "y", "t")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Title != tc.want || prs.created[0].Title != tc.want {
				t.Errorf("title = %q / %q, want %q", result.Title, prs.created[0].Title, tc.want)
			}
		})
	}
}

func TestCreateWarnsThatTheTitleIsFromOneIssue(t *testing.T) {
	client, _, _ := wrapped(claimed(46, "first"), claimed(50, "second"))
	result, err := client.Create(t.Context(), CreateRequest{
		For: []int{46, 50}, Body: Body{Sections: sections("w", "y", "t")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range result.Warnings {
		if w.Kind == WarnPrimaryTitle {
			if w.Issue != 46 {
				t.Errorf("Issue = %d, want the primary", w.Issue)
			}
			return
		}
	}
	t.Fatalf("warnings = %+v, want the primary-title warning", result.Warnings)
}

func TestCreateExplainsWhyASetHasNoSectionDefaults(t *testing.T) {
	client, _, _ := wrapped(claimed(46, "first"), claimed(50, "second"))
	result, err := client.Create(t.Context(), CreateRequest{
		For: []int{46, 50}, Body: Body{Sections: sections("", "", "t")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range result.Warnings {
		if w.Kind == WarnNoSectionDefaults {
			if !slices.Equal(w.Issues, []int{46, 50}) {
				t.Errorf("Issues = %v", w.Issues)
			}
			return
		}
	}
	t.Fatalf("warnings = %+v, want the no-defaults warning", result.Warnings)
}

// Exactly one Fixes line per declared issue, sorted, plus the epic the issues
// hang off — never a second copy and never a missing one.
func TestCreateWritesTheExactClosingSet(t *testing.T) {
	client, _, prs := wrapped(subIssueOf(claimed(50, "second"), 33), subIssueOf(claimed(46, "first"), 33))
	if _, err := client.Create(t.Context(), CreateRequest{
		For: []int{50, 46}, Body: Body{Sections: sections("w", "y", "t")},
	}); err != nil {
		t.Fatal(err)
	}
	body := prs.created[0].Body
	if !strings.HasSuffix(body, "Fixes #46\nFixes #50\nPart of #33") {
		t.Errorf("trailers = %q", body)
	}
}

func TestCreateRefusesATextBodyClosingAnotherSet(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "one"))
	_, err := client.Create(t.Context(), CreateRequest{Body: Body{Text: "Fixes #99"}})
	assertInvalid(t, err, "trailers")
	if len(prs.created) != 0 {
		t.Error("a pull request was created with a mismatched closing set")
	}
}

func TestCreateRequiresAPullRequestBackend(t *testing.T) {
	store := &fakeStore{issues: []model.Issue{claimed(30, "one")}, viewer: "me"}
	client := Wrap(store, nil, gh.Repo{Owner: "o", Name: "r"}, git.State{Branch: "feat/x", Pushed: true})
	_, err := client.Create(t.Context(), CreateRequest{})
	var noPRs *NoPullRequestsError
	if !errors.As(err, &noPRs) {
		t.Fatalf("err = %T (%v), want *NoPullRequestsError", err, err)
	}
}

// A composition that succeeded is worth reporting even when the write fails:
// the warnings tell whoever inspects the failure what the body was missing.
func TestCreateKeepsItsAdviceWhenTheWriteFails(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "one"))
	cause := errors.New("boom")
	prs.createErr = cause
	result, err := client.Create(t.Context(), CreateRequest{Body: Body{Sections: sections("w", "y", "")}})
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the backend cause", err)
	}
	if len(result.Warnings) == 0 || result.Warnings[0].Kind != WarnMissingTesting {
		t.Errorf("warnings = %+v, want the Testing advice", result.Warnings)
	}
}

func TestCreatePropagatesBackendFailures(t *testing.T) {
	cause := errors.New("boom")
	t.Run("list", func(t *testing.T) {
		client, store, _ := wrapped(claimed(30, "one"))
		store.listErr = cause
		if _, err := client.Create(t.Context(), CreateRequest{}); !errors.Is(err, cause) {
			t.Fatalf("err = %v, want the cause", err)
		}
	})
	t.Run("viewer", func(t *testing.T) {
		client, store, _ := wrapped(claimed(30, "one"))
		store.viewerErr = &gh.AuthError{Err: cause}
		_, err := client.Create(t.Context(), CreateRequest{})
		var auth *tracker.AuthError
		if !errors.As(err, &auth) || !errors.Is(err, cause) {
			t.Fatalf("err = %T (%v), want a *tracker.AuthError retaining the cause", err, err)
		}
	})
	t.Run("pull request context", func(t *testing.T) {
		client, _, prs := wrapped(claimed(30, "one"))
		prs.contextErr = cause
		if _, err := client.Create(t.Context(), CreateRequest{}); !errors.Is(err, cause) {
			t.Fatalf("err = %v, want the cause", err)
		}
	})
}

// A write's auth failure reaches the consumer as the public tracker type, not
// the API layer's error it cannot name.
func TestCreateMapsAWriteAuthFailure(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "one"))
	cause := errors.New("token expired")
	prs.createErr = &gh.AuthError{Err: cause}
	_, err := client.Create(t.Context(), CreateRequest{})
	var auth *tracker.AuthError
	if !errors.As(err, &auth) || !errors.Is(err, cause) {
		t.Fatalf("err = %T (%v), want a *tracker.AuthError retaining the cause", err, err)
	}
}

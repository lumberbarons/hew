package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func TestTriageUnicodeFindings(t *testing.T) {
	for _, search := range []string{"", "report"} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("search=%q/json=%v", search, asJSON), func(t *testing.T) {
				badTitle := issue(1, "Report p\u0430ypal\u200b")
				badBody := issue(2, "Report with hidden body")
				badBody.Body = "\u202e\U000e0069"
				clean := issue(3, "Report with emoji 👩‍💻")
				vetted := issue(4, "Vetted report\u200b", "P2", "bug")
				app, out, stderr := newApp(newFake(badTitle, badBody, clean, vetted))
				app.JSON = asJSON
				if err := app.Triage(ctx, TriageOpts{Search: search}); err != nil {
					t.Fatal(err)
				}
				if stderr.Len() != 0 {
					t.Errorf("findings should annotate issues, got stderr: %s", stderr.String())
				}
				lines := strings.Split(strings.TrimSpace(out.String()), "\n")
				if len(lines) != 3 || strings.Contains(out.String(), vetted.Title) {
					t.Fatalf("triage membership changed: %s", out.String())
				}
				if asJSON {
					want := []model.TextFindings{
						{ZeroWidth: true, Confusable: true},
						{BidiControl: true, UnicodeTags: true},
						{},
					}
					for n, line := range lines {
						var got struct {
							Number       int                `json:"number"`
							TextFindings model.TextFindings `json:"textFindings"`
						}
						if err := json.Unmarshal([]byte(line), &got); err != nil {
							t.Fatal(err)
						}
						if got.Number != n+1 || got.TextFindings != want[n] {
							t.Errorf("line %d: %s", n, line)
						}
					}
				} else {
					for n, want := range []string{
						"[contains zero-width characters; contains confusable characters]",
						"[contains bidi controls; contains Unicode tags]",
					} {
						if !strings.Contains(lines[n], want) {
							t.Errorf("line %d missing %q: %s", n, want, lines[n])
						}
					}
				}
				if strings.Contains(lines[2], "contains ") || strings.Contains(lines[2], "textFindings") {
					t.Errorf("clean emoji report was flagged: %s", lines[2])
				}
			})
		}
	}
}

func TestShowUnicodeFindings(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", asJSON), func(t *testing.T) {
			// Untriaged on purpose: show is the deliberate read path for them.
			i := issue(1, "Report p\u0430ypal")
			i.Body = "Steps\u200b to reproduce"
			i.Comments = []model.Comment{
				{Author: "alice", Body: "same here 👩‍💻"},
				{Author: "mallory", Body: "a\u202eb"},
			}
			i.CommentsTotal = 4
			app, out, stderr := newApp(newFake(i))
			app.JSON = asJSON
			if err := app.Show(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Errorf("findings should annotate the issue, got stderr: %s", stderr.String())
			}
			if !asJSON {
				for _, want := range []string{
					"  title: contains confusable characters\n",
					"  body: contains zero-width characters\n",
					"  comment 2 (@mallory): contains bidi controls\n",
					"comments (showing last 2 of 4):",
					i.Body,
				} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("show missing %q:\n%s", want, out.String())
					}
				}
				return
			}
			var got struct {
				Body         string `json:"body"`
				TextFindings []struct {
					Source       string `json:"source"`
					CommentIndex *int   `json:"commentIndex"`
					model.TextFindings
				} `json:"textFindings"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Body != i.Body || len(got.TextFindings) != 3 {
				t.Fatalf("JSON = %s", out.String())
			}
			title, body, comment := got.TextFindings[0], got.TextFindings[1], got.TextFindings[2]
			if title.Source != "title" || title.TextFindings != (model.TextFindings{Confusable: true}) || title.CommentIndex != nil ||
				body.Source != "body" || body.TextFindings != (model.TextFindings{ZeroWidth: true}) || body.CommentIndex != nil ||
				comment.Source != "comment" || comment.TextFindings != (model.TextFindings{BidiControl: true}) ||
				comment.CommentIndex == nil || *comment.CommentIndex != 1 {
				t.Errorf("textFindings = %s", out.String())
			}
		})
	}
}

func TestShowCleanIssueHasNoFindings(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		i := issue(1, "Clean 👩‍💻 report", "P2", "bug")
		i.Body = "café ✅"
		i.Comments = []model.Comment{{Author: "alice", Body: "👍🏽"}}
		app, out, _ := newApp(newFake(i))
		app.JSON = asJSON
		if err := app.Show(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "contains ") || strings.Contains(out.String(), "textFindings") {
			t.Errorf("JSON=%v clean issue was flagged: %s", asJSON, out.String())
		}
	}
}

func TestMutationsDoNotAnnotateUnicode(t *testing.T) {
	app, out, _ := newApp(newFake(issue(1, "Report p\u0430ypal\u200b", "P2", "bug")))
	app.JSON = true
	if err := app.Start(ctx, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "textFindings") {
		t.Errorf("start --json scans outside show and triage: %s", out.String())
	}
}

func TestOtherReadsDoNotAnnotateUnicode(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		app, out, stderr := newApp(newFake(issue(1, "Report p\u0430ypal\u200b", "P2", "bug")))
		app.JSON = asJSON
		for _, command := range []struct {
			name string
			run  func() error
		}{
			{"ready", func() error { return app.Ready(ctx, ReadyOpts{}) }},
			{"prime", func() error { return app.Prime(ctx, PrimeOpts{}) }},
			{"list", func() error { return app.List(ctx, ListOpts{}) }},
			{"search", func() error { return app.Search(ctx, "report") }},
		} {
			out.Reset()
			stderr.Reset()
			if err := command.run(); err != nil {
				t.Fatal(err)
			}
			for _, unwanted := range []string{"contains zero-width", "contains confusable", "textFindings"} {
				if strings.Contains(out.String()+stderr.String(), unwanted) {
					t.Errorf("%s JSON=%v scans outside triage: %s", command.name, asJSON, out.String())
				}
			}
		}
	}
}

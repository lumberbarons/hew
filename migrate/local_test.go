package migrate

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

func TestRewriteLocalRefs(t *testing.T) {
	m := map[int]int{1: 101, 12: 112, 3: 103}
	for name, tc := range map[string]struct{ in, want string }{
		"plain":                                {"fixes #1", "fixes #101"},
		"start and end":                        {"#1", "#101"},
		"punctuation":                          {"(#1), #12; #3.", "(#101), #112; #103."},
		"adjacent":                             {"#1 #12 #3", "#101 #112 #103"},
		"not in mapping":                       {"see #2 and #99", "see #2 and #99"},
		"longer number":                        {"#123 is not #12", "#123 is not #112"},
		"inline code":                          {"`#1` but #1", "`#1` but #101"},
		"double backticks":                     {"``a ` #1`` then #1", "``a ` #1`` then #101"},
		"unterminated tick":                    {"a ` then #1", "a ` then #101"},
		"fenced block":                         {"#1\n```\n#1\n```\n#1", "#101\n```\n#1\n```\n#101"},
		"tilde fence":                          {"~~~\n#1\n~~~\n#1", "~~~\n#1\n~~~\n#101"},
		"longer closing":                       {"```\n#1\n`````\n#1", "```\n#1\n`````\n#101"},
		"unclosed fence":                       {"#1\n```\n#1\n#12", "#101\n```\n#1\n#12"},
		"fence in list item":                   {"- a #1\n  ```\n  #1\n  ```\n  b #1\n", "- a #101\n  ```\n  #1\n  ```\n  b #101\n"},
		"fence in tab indented item":           {"- a\n\t```\n\t#1\n\t```\n\t#1", "- a\n\t```\n\t#1\n\t```\n\t#101"},
		"indented code after thematic break":   {"***\n    #1\nb #1", "***\n    #1\nb #101"},
		"indented code after spaced break":     {"a\n\n- - -\n    #1", "a\n\n- - -\n    #1"},
		"indented code after underscore break": {"___\n    #1", "___\n    #1"},
		"indented code after setext underline": {"Title\n=====\n    #1\nb #1", "Title\n=====\n    #1\nb #101"},
		"indented code after dash break":       {"Title\n---\n    #1", "Title\n---\n    #1"},
		"indented code after break in item":    {"- a\n  ***\n      #1\n  b #1", "- a\n  ***\n      #1\n  b #101"},
		"mixed dashes are not a break":         {"-*-\n    #1", "-*-\n    #101"},
		"two dashes are not a break":           {"--\n    #1", "--\n    #101"},
		"crlf fence in item":                   {"- a\r\n  ```\r\n  #1\r\n  ```\r\n  #1\r\n", "- a\r\n  ```\r\n  #1\r\n  ```\r\n  #101\r\n"},
		"crlf indented code":                   {"a\r\n\r\n    #1\r\n\r\nb #1", "a\r\n\r\n    #1\r\n\r\nb #101"},
		"crlf thematic break":                  {"***\r\n    #1", "***\r\n    #1"},
		"tab after marker":                     {"-\tfoo\n\n\t\t#1\n\tb #1", "-\tfoo\n\n\t\t#1\n\tb #101"},
		"tab after marker is code":             {"-\t\t#1\n  b #1", "-\t\t#1\n  b #101"},
		"fence in numbered item":               {"1. a\n   ~~~\n   #1\n   ~~~\n   #1", "1. a\n   ~~~\n   #1\n   ~~~\n   #101"},
		"fence in nested list":                 {"- a\n  - b\n    ```\n    #1\n    ```\n  - #1", "- a\n  - b\n    ```\n    #1\n    ```\n  - #101"},
		"fence opened on the marker line":      {"- ```\n  #1\n  ```\n#1", "- ```\n  #1\n  ```\n#101"},
		"list fence blank line inside":         {"- ```\n  #1\n\n  #1\n  ```\n  #1", "- ```\n  #1\n\n  #1\n  ```\n  #101"},
		"list fence ends with the item":        {"- ```\n  #1\nafter #1", "- ```\n  #1\nafter #101"},
		"fence in blockquote":                  {"> a #1\n> ```\n> #1\n> ```\n> b #1\n", "> a #101\n> ```\n> #1\n> ```\n> b #101\n"},
		"blockquote fence ends with the quote": {"> ```\n> #1\n\n#1", "> ```\n> #1\n\n#101"},
		"fence in quote in list":               {"- > ```\n  > #1\n  > ```\n  > #1", "- > ```\n  > #1\n  > ```\n  > #101"},
		"indented code":                        {"a #1\n\n    #1\n    #12\n\nb #1", "a #101\n\n    #1\n    #12\n\nb #101"},
		"tab indented code":                    {"a\n\n\t#1\n\nb #1", "a\n\n\t#1\n\nb #101"},
		"indented code at start":               {"    #1\nthen #1", "    #1\nthen #101"},
		"indented code with blank between":     {"a\n\n    #1\n\n    #1\n\nb #1", "a\n\n    #1\n\n    #1\n\nb #101"},
		"indented code after heading":          {"# T\n    #1\nb #1", "# T\n    #1\nb #101"},
		"indented code in list item":           {"- a\n\n      #1\n\n  b #1", "- a\n\n      #1\n\n  b #101"},
		"indented code on the marker line":     {"-     #1\n  b #1", "-     #1\n  b #101"},
		"indented code in blockquote":          {"> a\n>\n>     #1\n> b #1", "> a\n>\n>     #1\n> b #101"},
		"indented line continues a paragraph":  {"a\n    #1", "a\n    #101"},
		"item continuation is prose":           {"- a\n\n    b #1", "- a\n\n    b #101"},
		"sibling and nested items":             {"- #1\n  - #12\n- #3", "- #101\n  - #112\n- #103"},
		"numbered and quoted prose":            {"1) #1\n> #12", "1) #101\n> #112"},
		"not a marker":                         {"-x\n1234567890. y\n1.z\n+\n#1", "-x\n1234567890. y\n1.z\n+\n#101"},
		"url fragment":                         {"https://x.test/page#1 and a/#3", "https://x.test/page#1 and a/#3"},
		"word glued":                           {"foo#1 and abc_#1", "foo#1 and abc_#1"},
		"html entity":                          {"&#12; is a char", "&#12; is a char"},
		"followed by letter":                   {"#1a and #1_ and #1x", "#1a and #1_ and #1x"},
		"heading":                              {"# 1 title\n## #1", "# 1 title\n## #101"},
		"empty":                                {"", ""},
		"crlf":                                 {"a #1\r\nb #12\r\n", "a #101\r\nb #112\r\n"},
		"unicode":                              {"héllo #1 wörld", "héllo #101 wörld"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rewriteLocalRefs(tc.in, m); got != tc.want {
				t.Errorf("rewriteLocalRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRewriteLocalRefsInlineBlockBoundaries(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"sibling list items":       {"- stray ` #1\n- next ` #1", "- stray ` #101\n- next ` #101"},
		"numbered list items":      {"1. stray ` #1\n2. next ` #1", "1. stray ` #101\n2. next ` #101"},
		"nested list item":         {"- stray ` #1\n  - next ` #1", "- stray ` #101\n  - next ` #101"},
		"leaving list item":        {"- stray ` #1\n# after ` #1", "- stray ` #101\n# after ` #101"},
		"entering quote":           {"stray ` #1\n> next ` #1", "stray ` #101\n> next ` #101"},
		"leaving quote":            {"> stray ` #1\n# after ` #1", "> stray ` #101\n# after ` #101"},
		"quote paragraphs":         {"> stray ` #1\n>\n> next ` #1", "> stray ` #101\n>\n> next ` #101"},
		"crlf quote paragraphs":    {"> stray ` #1\r\n> \r\n> next ` #1", "> stray ` #101\r\n> \r\n> next ` #101"},
		"nested quote paragraphs":  {"> > stray ` #1\n> >\n> > next ` #1", "> > stray ` #101\n> >\n> > next ` #101"},
		"quoted list items":        {"> - stray ` #1\n> - next ` #1", "> - stray ` #101\n> - next ` #101"},
		"quote paragraphs in list": {"- > stray ` #1\n  >\n  > next ` #1", "- > stray ` #101\n  >\n  > next ` #101"},
		"inline code in list":      {"- `#1` and #1", "- `#1` and #101"},
		"multiline code in list":   {"- code `#1\n  #1` and #1", "- code `#1\n  #1` and #101"},
		"inline code in quote":     {"> `#1` and #1", "> `#1` and #101"},
		"multiline code in quote":  {"> code `#1\n> #1` and #1", "> code `#1\n> #1` and #101"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rewriteLocalRefs(tc.in, map[int]int{1: 101}); got != tc.want {
				t.Errorf("rewriteLocalRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRewriteLocalRefsParagraphContinuations(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"lazy list":                 {"- code `#1\n#1` and #1", "- code `#1\n#1` and #101"},
		"lazy quote":                {"> code `#1\n#1` and #1", "> code `#1\n#1` and #101"},
		"lazy nested quote":         {"> > code `#1\n> #1` and #1", "> > code `#1\n> #1` and #101"},
		"lazy quote in list":        {"- > code `#1\n#1` and #1", "- > code `#1\n#1` and #101"},
		"partly indented list":      {"1. code `#1\n #1` and #1", "1. code `#1\n #1` and #101"},
		"explicit after lazy quote": {"> code `#1\nlazy #1\n> #1` and #1", "> code `#1\nlazy #1\n> #1` and #101"},
		"explicit after lazy list":  {"- code `#1\nlazy #1\n  #1` and #1", "- code `#1\nlazy #1\n  #1` and #101"},
		"crlf lazy quote":           {"> code `#1\r\n#1` and #1", "> code `#1\r\n#1` and #101"},
		"indented lazy heading":     {"> code `#1\n    # heading #1` and #1", "> code `#1\n    # heading #1` and #101"},
		"indented lazy underline":   {"> code `#1\n    ===\n#1` and #1", "> code `#1\n    ===\n#1` and #101"},
		"numbered prose":            {"text `#1\n2. #1` and #1", "text `#1\n2. #1` and #101"},
		"parenthesized prose":       {"text `#1\n2) #1` and #1", "text `#1\n2) #1` and #101"},
		"numbered prose in quote":   {"> code `#1\n> 2. #1` and #1", "> code `#1\n> 2. #1` and #101"},
		"numbered prose in list":    {"- code `#1\n  2. #1` and #1", "- code `#1\n  2. #1` and #101"},
		"lazy numbered prose":       {"> code `#1\n2. #1` and #1", "> code `#1\n2. #1` and #101"},
		"different ordered marker":  {"1. code `#1\n2) #1` and #1", "1. code `#1\n2) #1` and #101"},
		"empty bullet in prose":     {"text `#1\n+\n#1` and #1", "text `#1\n+\n#1` and #101"},
		"first numbered item":       {"text ` #1\n1. next ` #1", "text ` #101\n1. next ` #101"},
		"list after lazy line":      {"- stray ` #1\nlazy #1\n- next ` #1", "- stray ` #101\nlazy #101\n- next ` #101"},
		"quote after lazy line":     {"> > stray ` #1\nlazy #1\n>\n> next ` #1", "> > stray ` #101\nlazy #101\n>\n> next ` #101"},
		"numbered item after blank": {"text ` #1\n\n2. next ` #1", "text ` #101\n\n2. next ` #101"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rewriteLocalRefs(tc.in, map[int]int{1: 101}); got != tc.want {
				t.Errorf("rewriteLocalRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLocalCommentCreditsAnAuthorSafely(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for name, tc := range map[string]struct{ author, want string }{
		"login":      {"dev", "`dev` commented on 2026-03-04:\n\nhello"},
		"backticks":  {"a`b", "`a'b` commented on 2026-03-04:\n\nhello"},
		"no author":  {"", "`unknown` commented on 2026-03-04:\n\nhello"},
		"whitespace": {"  dev ", "`dev` commented on 2026-03-04:\n\nhello"},
	} {
		if got := localComment(local.Comment{Author: tc.author, CreatedAt: at, Body: "hello"}); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestLocalLabelsSkipsConventionLabelsAndRepeats(t *testing.T) {
	snap := &local.Snapshot{
		Labels: []gh.Label{{Name: "Area-X", Color: "111111"}, {Name: "P1", Color: "222222"}},
		Issues: []local.Issue{
			{Labels: []string{"area-x", "extra", "EXTRA", "P1", "bug"}},
			{Labels: []string{"extra"}},
		},
	}
	got := localLabels(snap)
	var names []string
	for _, l := range got {
		names = append(names, l.Name)
	}
	if !slices.Equal(names, []string{"Area-X", "extra"}) {
		t.Fatalf("labels = %v, want each custom label once, convention labels left to the bootstrap", names)
	}
	if got[0].Color != "111111" || got[1].Color != "ededed" {
		t.Errorf("colors = %q, %q, want the local definition and a neutral default", got[0].Color, got[1].Color)
	}
}

func TestLocalItemsDropAStrayCaseClaimFromClosedIssues(t *testing.T) {
	snap := &local.Snapshot{Issues: []local.Issue{
		{Number: 1, State: "CLOSED", Labels: []string{"In-Progress", "P2"}},
	}}
	if got := localItems(snap)[0].Labels; slices.ContainsFunc(got, model.IsInProgressLabel) {
		t.Errorf("closed item labels = %v, want no claim label in any case", got)
	}
}

func TestLocalItemsDropTheClaimFromClosedIssues(t *testing.T) {
	snap := &local.Snapshot{Issues: []local.Issue{
		{Number: 1, State: "CLOSED", StateReason: "REOPENED", Labels: []string{"in-progress", "P2"}, Assignees: []string{"dev-wt"}},
		{Number: 2, State: "OPEN", Labels: []string{"in-progress"}, Assignees: []string{"dev-wt", "other"}},
	}}
	items := localItems(snap)
	if slices.Contains(items[0].Labels, "in-progress") || len(items[0].Assignees) != 0 {
		t.Errorf("closed item = %+v, want no claim", items[0])
	}
	if items[0].CloseReason != "" {
		t.Errorf("close reason = %q, want the engine's default for an unrecognised reason", items[0].CloseReason)
	}
	if !slices.Equal(items[1].Assignees, []string{AssigneeViewer}) {
		t.Errorf("open item assignees = %v, want one claim for the migrating user", items[1].Assignees)
	}
}

// A local tracker predating the comma rule may hold an area like "x,y"; it
// migrates as "x-y" so it cannot read as two areas on GitHub.
func TestAreaLabelsSkipConventionLabelsInAnyCase(t *testing.T) {
	got := areaLabels([]string{"Bug", "p1", "IN-Progress", "web-ui"})
	if !slices.Equal(got, []string{"web-ui"}) {
		t.Errorf("areaLabels = %v, want only web-ui", got)
	}
}

func TestLocalRequiresATrackerDirectory(t *testing.T) {
	m := Wrap(newFakeTarget(), Config{Checkpoint: tracker.NewMemoryCheckpoint()})
	_, err := m.Local(context.Background(), LocalRequest{})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "dir" {
		t.Fatalf("err = %T %v, want an invalid dir", err, err)
	}
}

func TestLocalRequiresATargetForARealRun(t *testing.T) {
	m := Wrap(nil, Config{Checkpoint: tracker.NewMemoryCheckpoint()})
	_, err := m.Local(context.Background(), LocalRequest{Dir: filepath.Join(t.TempDir(), local.DirName)})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "target" {
		t.Fatalf("err = %T %v, want an invalid target", err, err)
	}
}

func TestLocalRequiresARepoForARealRun(t *testing.T) {
	worktree := t.TempDir()
	gitInit(t, worktree)
	dir := filepath.Join(worktree, local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	m := Wrap(newFakeTarget(), Config{Checkpoint: tracker.NewMemoryCheckpoint()})
	_, err := m.Local(context.Background(), LocalRequest{Dir: dir})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "repo" {
		t.Fatalf("err = %T %v, want an invalid repo", err, err)
	}
}

func TestLocalNamesATrackerThatIsNotLocal(t *testing.T) {
	m := Wrap(newFakeTarget(), Config{Checkpoint: tracker.NewMemoryCheckpoint(), DryRun: true})
	_, err := m.Local(context.Background(), LocalRequest{Dir: filepath.Join(t.TempDir(), local.DirName)})
	var notLocal *NotLocalError
	if !errors.As(err, &notLocal) || !strings.Contains(err.Error(), "hew init --local") {
		t.Fatalf("err = %T %v, want the not-local remedy", err, err)
	}
}

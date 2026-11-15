package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"
)

// The CLI surface — every command, flag name and positional shape — is
// contract, the same as exit codes and --json output: scripts and agent loops
// bind to it. This file snapshots it into testdata/surface.golden and fails
// when a command line that used to be accepted no longer is.
//
// Regenerate after an intended change with:
//
//	go test ./cmd/hew -run TestSurfaceCompat -update
//
// The policy the gate enforces is in DESIGN.md ("CLI surface compatibility").

var updateSurface = flag.Bool("update", false, "rewrite cmd/hew/testdata/surface.golden")

const surfaceGolden = "testdata/surface.golden"

// surfaceNote separates an entry's compat key from its annotation. The key —
// everything before the note — is what the differ compares; the annotation is
// there for the human reading a golden diff (which spelling is primary) and
// must never decide pass or fail, or demoting a flag to an alias of its
// replacement would look like a removal.
const surfaceNote = " ["

// surface serializes the command tree into canonical sorted entries, one per
// line-worth of contract: each command path, each command's positional usage,
// and each name a flag answers to (the primary and every alias) with its type
// and repeatability. A flag is one entry per accepted name so that keeping
// the old spelling as an alias leaves its entry in place — which is exactly
// what makes an alias the compatibility mechanism rather than a loophole.
func surface(root *ucli.Command) []string {
	var out []string
	var visit func(c *ucli.Command, path string)
	visit = func(c *ucli.Command, path string) {
		out = append(out, "command "+path)
		if c.ArgsUsage != "" {
			out = append(out, fmt.Sprintf("args %s %s", path, c.ArgsUsage))
		}
		for _, f := range c.Flags {
			names := f.Names()
			for i, name := range names {
				entry := fmt.Sprintf("flag %s %s %s", path, flagSpelling(name), flagShape(f))
				if i > 0 {
					entry += surfaceNote + "alias of " + flagSpelling(names[0]) + "]"
				}
				out = append(out, entry)
			}
		}
		for _, sub := range c.Commands {
			// A command alias is a second accepted spelling of the path.
			// It is recorded as its own entry so dropping it fails; only the
			// primary spelling's subtree is walked, since flags and
			// positionals belong to the command, not to the spelling.
			for i, name := range append([]string{sub.Name}, sub.Aliases...) {
				if i > 0 {
					out = append(out, "command "+path+" "+name+surfaceNote+"alias of "+sub.Name+"]")
					continue
				}
				visit(sub, path+" "+name)
			}
		}
	}
	visit(root, root.Name)
	slices.Sort(out)
	return out
}

func flagSpelling(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// flagShape is the type and arity a caller depends on: `--limit 5` and
// `--limit` are different command lines, and so are `--area a` once and
// repeated.
func flagShape(f ucli.Flag) string {
	doc, ok := f.(ucli.DocGenerationFlag)
	if !ok {
		return "unknown"
	}
	shape := doc.TypeName()
	if !doc.TakesValue() {
		shape = "bool"
	}
	if mv, ok := f.(ucli.DocGenerationMultiValueFlag); ok && mv.IsMultiValueFlag() {
		shape += " repeatable"
	}
	return shape
}

func surfaceKey(entry string) string {
	key, _, _ := strings.Cut(entry, surfaceNote)
	return key
}

// diffSurface compares a committed surface with a regenerated one by key.
// removed are entries the old surface accepted that the new one does not —
// the failures; added are new entries, which are always allowed. A rename
// without an alias appears as one removal plus one addition; with an alias the
// old name's key is still present and nothing is removed. Widening a flag to
// repeatable is the one shape change that keeps every old command line valid,
// so it is not a removal; narrowing back is.
func diffSurface(old, cur []string) (removed, added []string) {
	have := func(set []string) map[string]bool {
		m := make(map[string]bool, len(set))
		for _, e := range set {
			m[surfaceKey(e)] = true
		}
		return m
	}
	oldKeys, curKeys := have(old), have(cur)
	for _, e := range old {
		key := surfaceKey(e)
		// A single-value flag that became repeatable still accepts every
		// command line it did: `--on 5` parses either way.
		if !curKeys[key] && !curKeys[key+" repeatable"] {
			removed = append(removed, e)
		}
	}
	for _, e := range cur {
		if !oldKeys[surfaceKey(e)] {
			added = append(added, e)
		}
	}
	return removed, added
}

func TestSurfaceCompat(t *testing.T) {
	cur := surface(root())
	body := strings.Join(cur, "\n") + "\n"

	if *updateSurface {
		if err := os.MkdirAll(filepath.Dir(surfaceGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(surfaceGolden, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	raw, err := os.ReadFile(surfaceGolden)
	if err != nil {
		t.Fatalf("missing golden (run go test ./cmd/hew -run TestSurfaceCompat -update): %v", err)
	}
	old := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")

	removed, _ := diffSurface(old, cur)
	if len(removed) > 0 {
		t.Errorf("CLI surface lost entries a caller could rely on:\n  %s\n"+
			"A rename keeps the old spelling as an alias; a removal waits for a major release "+
			"(DESIGN.md, \"CLI surface compatibility\"). If this is an intended breaking change, "+
			"regenerate with: go test ./cmd/hew -run TestSurfaceCompat -update",
			strings.Join(removed, "\n  "))
	}
}

func TestSurfaceCoversTheTree(t *testing.T) {
	got := strings.Join(surface(root()), "\n")
	for _, want := range []string{
		"command hew",
		"command hew epic create",
		"flag hew list --state ",
		"flag hew list --closed bool",
		"flag hew create --done-when string repeatable",
		"args hew start <n>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("surface is missing %q", want)
		}
	}
}

func TestDiffSurface(t *testing.T) {
	base := []string{
		"command hew list",
		"flag hew list --closed bool",
		"flag hew list --limit int",
		"args hew show <n>",
	}
	cases := []struct {
		name        string
		old         []string // the committed surface; base when nil
		cur         []string
		wantRemoved []string
		wantAdded   []string
	}{
		{
			name: "unchanged",
			cur:  base,
		},
		{
			name:        "removal",
			cur:         []string{"command hew list", "flag hew list --limit int", "args hew show <n>"},
			wantRemoved: []string{"flag hew list --closed bool"},
		},
		{
			name: "rename without an alias is a removal and an addition",
			cur: []string{
				"command hew list", "flag hew list --finished bool",
				"flag hew list --limit int", "args hew show <n>",
			},
			wantRemoved: []string{"flag hew list --closed bool"},
			wantAdded:   []string{"flag hew list --finished bool"},
		},
		{
			name: "rename keeping the old spelling as an alias is not a removal",
			cur: []string{
				"command hew list", "flag hew list --finished bool",
				"flag hew list --closed bool [alias of --finished]",
				"flag hew list --limit int", "args hew show <n>",
			},
			wantAdded: []string{"flag hew list --finished bool"},
		},
		{
			// `--on 5` still parses when --on may repeat, so widening a
			// single-value flag costs no caller anything; the reverse does.
			name: "a flag that becomes repeatable is not a removal",
			cur: []string{
				"command hew list", "flag hew list --closed bool",
				"flag hew list --limit int repeatable", "args hew show <n>",
			},
			wantAdded: []string{"flag hew list --limit int repeatable"},
		},
		{
			name: "a flag that stops being repeatable is a removal",
			cur: []string{
				"command hew list", "flag hew list --closed bool",
				"flag hew list --limit int", "args hew show <n>",
			},
			old:         []string{"command hew list", "flag hew list --closed bool", "flag hew list --limit int repeatable", "args hew show <n>"},
			wantRemoved: []string{"flag hew list --limit int repeatable"},
			wantAdded:   []string{"flag hew list --limit int"},
		},
		{
			name:      "addition passes",
			cur:       append(slices.Clone(base), "flag hew list --all bool", "command hew archive"),
			wantAdded: []string{"flag hew list --all bool", "command hew archive"},
		},
		{
			name: "changed type is a removal of the old shape",
			cur: []string{
				"command hew list", "flag hew list --closed bool",
				"flag hew list --limit string", "args hew show <n>",
			},
			wantRemoved: []string{"flag hew list --limit int"},
			wantAdded:   []string{"flag hew list --limit string"},
		},
		{
			name: "changed positional is a removal",
			cur: []string{
				"command hew list", "flag hew list --closed bool",
				"flag hew list --limit int", "args hew show <id>",
			},
			wantRemoved: []string{"args hew show <n>"},
			wantAdded:   []string{"args hew show <id>"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := base
			if tc.old != nil {
				old = tc.old
			}
			removed, added := diffSurface(old, tc.cur)
			if !slices.Equal(removed, tc.wantRemoved) {
				t.Errorf("removed = %q, want %q", removed, tc.wantRemoved)
			}
			if !slices.Equal(added, tc.wantAdded) {
				t.Errorf("added = %q, want %q", added, tc.wantAdded)
			}
		})
	}
}

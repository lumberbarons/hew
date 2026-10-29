package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/hew/internal/cli"
)

// runHew runs the whole binary entry point against args and returns the exit
// code, stdout and stderr. A malformed --repo is a tripwire: a command that
// reaches buildApp fails with a usage error rather than calling GitHub.
func runHew(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), append([]string{"hew", "--repo", "invalid"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUnknownCommandExitsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"}, {"epic", "bogus"}, {"hooks", "bogus"}, {"migrate", "bogus"},
		{"help", "bogus"}, {"epic", "help", "bogus"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, stderr := runHew(t, args...)
			if code != appcli.ExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, appcli.ExitUsage, stderr)
			}
			if strings.Count(strings.TrimSpace(stderr), "\n") != 0 || !strings.Contains(stderr, "bogus") {
				t.Errorf("stderr = %q, want one line naming the unknown command", stderr)
			}
		})
	}
}

func TestExitCodeMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"start's claim refusal keeps 3", &appcli.ExitError{Code: appcli.ExitClaimed}, appcli.ExitClaimed},
		{"start's own-claim refusal keeps 5", &appcli.ExitError{Code: appcli.ExitClaimedByYou}, appcli.ExitClaimedByYou},
		{"start's blocked refusal keeps 6", &appcli.ExitError{Code: appcli.ExitBlocked}, appcli.ExitBlocked},
		{"framework exit 3 reads as usage", ucli.Exit("No help topic", 3), appcli.ExitUsage},
		{"framework exit 1 reads as usage", ucli.Exit("bad", 1), appcli.ExitUsage},
		{"plain error stays generic", errTest("boom"), appcli.ExitGeneric},
		{"no error", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Fatalf("exitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestUnknownCommandPointsAtHelp(t *testing.T) {
	code, _, stderr := runHew(t, "epic", "bogus")
	if code != appcli.ExitUsage || !strings.Contains(stderr, "hew epic --help") {
		t.Fatalf("exit = %d, stderr = %q; want a pointer to `hew epic --help`", code, stderr)
	}
}

func TestBareGroupCommandsStillShowHelp(t *testing.T) {
	for _, args := range [][]string{{}, {"epic"}, {"hooks"}, {"migrate"}, {"help"}, {"epic", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := runHew(t, args...)
			if code != 0 || !strings.Contains(stdout, "USAGE") || stderr != "" {
				t.Fatalf("exit = %d, stdout = %q, stderr = %q; want help on stdout and exit 0", code, stdout, stderr)
			}
		})
	}
}

func TestFlagParseErrorsExitUsageWithOneLine(t *testing.T) {
	for _, args := range [][]string{
		{"ready", "--limit", "abc"},
		{"search", "--nope", "x"},
		{"list", "--nope"},
		{"epic", "create", "--nope"},
		{"epic", "--nope"},
		{"show", "1", "-z"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := runHew(t, args...)
			if code != appcli.ExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, appcli.ExitUsage, stderr)
			}
			if strings.Count(strings.TrimSpace(stderr), "\n") != 0 {
				t.Errorf("stderr = %q, want the message once on one line", stderr)
			}
			if !strings.Contains(stderr, "--help") {
				t.Errorf("stderr = %q, want a pointer to --help", stderr)
			}
			if strings.Contains(stdout+stderr, "USAGE") || strings.Contains(stderr, "Incorrect Usage") {
				t.Errorf("output includes a full help dump:\n%s%s", stdout, stderr)
			}
		})
	}
}

func TestMissingRequiredFlagExitsUsage(t *testing.T) {
	for _, tc := range []struct {
		args []string
		flag string
	}{
		{[]string{"block", "1"}, "on"},
		{[]string{"unblock", "1"}, "from"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			code, stdout, stderr := runHew(t, tc.args...)
			if code != appcli.ExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, appcli.ExitUsage, stderr)
			}
			if !strings.Contains(stderr, tc.flag) || strings.Contains(stdout+stderr, "USAGE") {
				t.Errorf("stderr = %q; want one line naming --%s and no help dump", stderr, tc.flag)
			}
		})
	}
}

// commandPaths lists every command path, groups and leaves alike.
func commandPaths(cmd *ucli.Command, prefix []string) [][]string {
	var out [][]string
	for _, sub := range cmd.Commands {
		path := append(append([]string{}, prefix...), sub.Name)
		out = append(out, path)
		out = append(out, commandPaths(sub, path)...)
	}
	return out
}

// Exit 3, 5 and 6 belong to `start`'s claim guard: agent loops read them as
// "pick the next ready item" or "resume". No other path may produce them, so
// every command takes a battery of bad invocations and none may land on them.
func TestOnlyStartProducesClaimExitCodes(t *testing.T) {
	// Some of these invocations are valid for some commands (`hooks install
	// bogus` reaches the filesystem), so run them where nothing real is at stake.
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	reserved := map[int]bool{appcli.ExitClaimed: true, appcli.ExitClaimedByYou: true, appcli.ExitBlocked: true}
	paths := commandPaths(root(), nil)
	if len(paths) == 0 {
		t.Fatal("no commands found")
	}
	bad := [][]string{{"--nope"}, {"bogus"}, {"bogus", "--nope"}, {"--limit", "abc"}, {"help", "bogus"}, {"1", "2", "3"}}
	for _, path := range paths {
		for _, extra := range bad {
			args := append(append([]string{}, path...), extra...)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				code, _, stderr := runHew(t, args...)
				if reserved[code] {
					t.Fatalf("exit = %d from %v; only start's claim guard may exit 3, 5 or 6 (stderr %q)", code, args, stderr)
				}
			})
		}
	}
}

// startGuardFuncs are the only functions allowed to name the reserved codes:
// the refusals `start` returns from its claim and blocker checks.
var startGuardFuncs = map[string]bool{"claimRefusal": true, "blockerRefusal": true}

// The command layer's only constructors of the reserved codes sit in start's
// refusal helpers; a stray one anywhere else would surface as a claim. This
// checks every non-test file, write.go included, by enclosing function.
func TestReservedExitCodesAreConstructedOnlyByStartGuard(t *testing.T) {
	files, err := filepath.Glob("../../internal/cli/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	reserved := map[string]bool{"ExitClaimed": true, "ExitClaimedByYou": true, "ExitBlocked": true}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || filepath.Base(f) == "app.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || startGuardFuncs[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.Ident:
					if reserved[n.Name] {
						t.Errorf("%s: %s references %s; only start's refusal helpers may produce it", fset.Position(n.Pos()), fn.Name.Name, n.Name)
					}
				case *ast.KeyValueExpr:
					// A literal exit code, e.g. &ExitError{Code: 3}, dodges the constants.
					if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Code" {
						if lit, ok := n.Value.(*ast.BasicLit); ok {
							t.Errorf("%s: %s sets Code to the literal %s; use the named exit constants", fset.Position(n.Pos()), fn.Name.Name, lit.Value)
						}
					}
				}
				return true
			})
		}
	}
}

// Command tokens measures what a read command costs an agent's context: the
// tokens hew emits against the tokens of the raw gh and GraphQL output an agent
// would otherwise have to ingest to answer the same question.
//
// Two steps, because only the first needs GitHub:
//
//	go run ./cmd/tokens capture --repo owner/name --show 138 --epic 137 --out fixtures/owner-name
//	go run ./cmd/tokens report fixtures/owner-name
//
// capture records both sides' raw output into a fixture directory alongside a
// manifest of the exact commands that produced it. report tokenizes that
// fixture — offline, deterministic, no model calls — so committed numbers stay
// reproducible and disputable.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const usage = `measure hew's read-command output against the raw gh equivalent

usage:
  tokens capture --repo owner/name [--show N] [--epic N] --out DIR [--hew PATH] [--queries DIR]
  tokens report DIR [--format text|markdown|json]

capture needs gh authenticated and reads the repo; report is offline.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("no subcommand given")
	}
	switch args[0] {
	case "capture":
		return runCapture(args[1:], stdout, stderr)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runCapture(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f Fixture
	var out string
	fs.StringVar(&f.Repo, "repo", "", "repository to capture, as owner/name")
	fs.IntVar(&f.ShowIssue, "show", 0, "issue number to compare `hew show` against")
	fs.IntVar(&f.EpicIssue, "epic", 0, "epic number to compare `hew epic status` against (omit if the repo has no epic)")
	fs.StringVar(&f.Hew, "hew", "hew", "hew binary to measure")
	fs.StringVar(&f.QueryDir, "queries", "cmd/tokens/queries", "directory holding the baseline GraphQL queries")
	fs.StringVar(&out, "out", "", "fixture directory to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if out == "" {
		return fmt.Errorf("--out is required")
	}

	m, err := capture(f, out, execRunner, time.Now(), stdout)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s: %d open issues, %d entries\n", out, m.OpenIssues, len(m.Entries))
	return nil
}

func runReport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "output format: text, markdown, or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs := fs.Args()
	if len(dirs) == 0 {
		return fmt.Errorf("give at least one fixture directory")
	}

	c, err := newCounter()
	if err != nil {
		return err
	}
	for i, dir := range dirs {
		rep, err := buildReport(dir, c)
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		if err := rep.Write(stdout, *format); err != nil {
			return err
		}
	}
	return nil
}

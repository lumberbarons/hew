package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"
)

// The README's deny guidance is load-bearing: it is the mechanical half of
// the untriaged boundary, so a rule set that lets any valid spelling of
// `hew triage` through reads like a guarantee it does not keep. These tests
// pin the documented rules to the real command surface.

// documentedDenyRules extracts the triage deny rules from the README section
// that recommends them, so the test fails when the docs and the checked-in
// recommendation drift apart rather than testing a copy.
func documentedDenyRules(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, m := range regexp.MustCompile("```json\n(?s)(.*?)```").FindAllSubmatch(b, -1) {
		var settings struct {
			Permissions struct {
				Deny []string `json:"deny"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(m[1], &settings); err != nil {
			continue
		}
		for _, r := range settings.Permissions.Deny {
			if strings.Contains(r, "triage") {
				rules = append(rules, r)
			}
		}
	}
	if len(rules) == 0 {
		t.Fatal("README's deny recommendation mentions no triage rule")
	}
	return rules
}

// matchesBashRule implements the wildcard semantics Claude Code documents
// for Bash permission rules: `*` matches any text, including spaces, and a
// rule ending in " *" also matches the bare command without the trailing
// space. Everything before a `*` is matched as written.
func matchesBashRule(rule, command string) bool {
	pattern, ok := strings.CutPrefix(rule, "Bash(")
	if !ok || !strings.HasSuffix(pattern, ")") {
		return false
	}
	pattern = strings.TrimSuffix(pattern, ")")
	if strings.HasSuffix(pattern, " *") {
		pattern = strings.TrimSuffix(pattern, " *") + "*"
	}
	if !strings.Contains(pattern, "*") {
		return pattern == command
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(command, parts[0]) {
		return false
	}
	rest := command[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		idx := strings.Index(rest, p)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(p):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// flagForms returns every token sequence a flag can appear as: "--name" for
// bools; "--name value" and "--name=value" for value-taking flags.
func flagForms(fl ucli.Flag) [][]string {
	name := fl.Names()[0]
	if _, ok := fl.(*ucli.BoolFlag); ok {
		return [][]string{{"--" + name}}
	}
	return [][]string{{"--" + name, "value"}, {"--" + name + "=value"}}
}

// validTriageArrangements enumerates every valid spelling of `hew triage`
// from the actual command tree: the global flags in either position (they
// are persistent, so urfave parses them before or after the subcommand) and
// triage's own flags after it. It exists so a flag added to the surface
// cannot silently reopen the deny boundary.
func validTriageArrangements(t *testing.T) []string {
	t.Helper()
	rootCmd := root()
	var elements [][][]string
	for _, fl := range rootCmd.Flags {
		elements = append(elements, flagForms(fl))
	}
	triage := findCommand(t, rootCmd, "triage")
	var leafFlagNames []string
	for _, fl := range triage.Flags {
		leafFlagNames = append(leafFlagNames, "--"+fl.Names()[0])
		elements = append(elements, flagForms(fl))
	}

	// Each element is optional; pick one form per present element, then
	// every ordering of the chosen elements.
	var candidates []string
	var build func(idx int, chosen [][]string)
	build = func(idx int, chosen [][]string) {
		if idx == len(elements) {
			for _, perm := range permutations(len(chosen)) {
				var tokens []string
				for _, at := range perm {
					tokens = append(tokens, chosen[at]...)
				}
				candidates = append(candidates, "hew "+strings.Join(tokens, " "))
			}
			return
		}
		build(idx+1, chosen)
		for _, form := range elements[idx] {
			build(idx+1, append(chosen, form))
		}
	}
	build(0, nil)

	// Keep only the arrangements hew actually accepts: triage's own flags
	// never precede the subcommand, so drop any ordering where a leaf flag
	// lands before it.
	var valid []string
	for _, cmd := range candidates {
		fields := strings.Fields(strings.TrimPrefix(cmd, "hew "))
		triageAt := slicesIndex(fields, "triage")
		if triageAt < 0 {
			continue
		}
		ok := true
		for i, f := range fields {
			if i >= triageAt {
				break
			}
			for _, name := range leafFlagNames {
				if strings.HasPrefix(f, name) {
					ok = false
				}
			}
		}
		if ok {
			valid = append(valid, cmd)
		}
	}
	return valid
}

func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, sub := range permutations(n - 1) {
		for i := 0; i <= len(sub); i++ {
			p := make([]int, 0, n)
			p = append(p, sub[:i]...)
			p = append(p, n-1)
			p = append(p, sub[i:]...)
			out = append(out, p)
		}
	}
	return out
}

func slicesIndex(s []string, want string) int {
	for i, v := range s {
		if v == want {
			return i
		}
	}
	return -1
}

func TestDenyRulesCoverEveryValidTriageArrangement(t *testing.T) {
	rules := documentedDenyRules(t)
	for _, cmd := range validTriageArrangements(t) {
		covered := false
		for _, rule := range rules {
			if matchesBashRule(rule, cmd) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("no documented deny rule matches %q", cmd)
		}
	}
}

func TestDenyRulesLeaveOtherCommandsAlone(t *testing.T) {
	// An over-broad recommendation (e.g. denying every `hew` call) would
	// satisfy the coverage test while crippling the coding agent; the rules
	// must scope to triage.
	rules := documentedDenyRules(t)
	for _, cmd := range []string{
		"hew ready",
		"hew list --json",
		"hew search retry",
		"hew show 7",
		"hew set 7 --priority P2",
		"hew start 7",
		"hew pr",
		"hew prime",
	} {
		for _, rule := range rules {
			if matchesBashRule(rule, cmd) {
				t.Errorf("deny rule %q blocks the benign command %q", rule, cmd)
			}
		}
	}
}

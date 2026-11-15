package model

import (
	"strconv"
	"strings"
)

// Cycles finds dependency cycles among open issues, following blocked-by
// edges. GitHub only rejects self-blocks and direct two-issue cycles, so
// longer cycles can exist (created via web UI or raw API) and must be
// detected client-side: every member of a cycle has an open blocker, so a
// cycle silently excludes all its members from ready forever.
//
// allOpen must be every open issue in the repository. Edges to issues not in
// the set are dropped as non-blocking, so passing a filtered subset yields
// false negatives — a cycle whose members were filtered out goes undetected.
//
// Each cycle is returned once as a slice of issue numbers in edge order
// (a blocked-by b blocked-by c ... blocked-by a), starting from its
// smallest member.
func Cycles(allOpen []Issue) [][]int {
	issues := allOpen
	adj := blockedByGraph(issues)

	const (
		white = 0 // unvisited
		gray  = 1 // on current DFS path
		black = 2 // done
	)
	color := make(map[int]int, len(adj))
	var path []int
	onPath := make(map[int]int) // number -> index in path
	var cycles [][]int
	seen := make(map[string]bool)

	var dfs func(n int)
	dfs = func(n int) {
		color[n] = gray
		onPath[n] = len(path)
		path = append(path, n)
		for _, m := range adj[n] {
			switch color[m] {
			case white:
				dfs(m)
			case gray:
				cyc := canonical(path[onPath[m]:])
				if key := cycleKey(cyc); !seen[key] {
					seen[key] = true
					cycles = append(cycles, cyc)
				}
			}
		}
		path = path[:len(path)-1]
		delete(onPath, n)
		color[n] = black
	}

	// Iterate in ascending number order for deterministic output.
	for _, i := range issues {
		if _, ok := adj[i.Number]; ok && color[i.Number] == white {
			dfs(i.Number)
		}
	}
	return cycles
}

// CycleCheck is the verdict on a prospective "issue blocked by blocker" edge.
type CycleCheck struct {
	// Cycle is the cycle the edge would close, in edge order starting at
	// issue, or nil if no cycle was found.
	Cycle []int
	// Verifiable is false when an issue reached during the search had more
	// blockers than were fetched: an unfetched blocker could complete a
	// cycle the search cannot see, so a nil Cycle must not be treated as
	// proof the edge is safe.
	Verifiable bool
}

// Checker is a prepared cycle check over one open-issue graph. Build it once
// to check several prospective edges without rebuilding the graph for each,
// which is what a multi-blocker write does.
type Checker struct {
	adj       map[int][]int
	truncated map[int]bool
}

// NewChecker prepares a check over allOpen. The set has the same requirement
// as CheckBlockedBy: it must be every open issue in the repository, or a
// cycle whose members were filtered out goes undetected.
func NewChecker(allOpen []Issue) *Checker {
	return &Checker{adj: blockedByGraph(allOpen), truncated: truncatedBlockers(allOpen)}
}

// CheckBlockedBy reports whether adding "issue blocked by blocker" would
// create a cycle. The check is transitive over blocked-by edges: the edge
// closes a cycle iff blocker is already (transitively) blocked by issue.
//
// allOpen must be every open issue in the repository (see Cycles). When any
// issue on the search path had a truncated blocker list the verdict is
// marked unverifiable, because the missing edges could hide a cycle.
func CheckBlockedBy(allOpen []Issue, issue, blocker int) CycleCheck {
	return NewChecker(allOpen).Check(issue, blocker)
}

// Check answers one edge against the prepared graph, with CheckBlockedBy's
// semantics. A found cycle is definite even when part of the graph is
// truncated; a nil cycle on a truncated path is not proof the edge is safe.
func (c *Checker) Check(issue, blocker int) CycleCheck {
	if issue == blocker {
		return CycleCheck{Cycle: []int{issue, issue}, Verifiable: true}
	}
	verifiable := true
	// Find a path blocker -> ... -> issue through blocked-by edges.
	visited := map[int]bool{blocker: true}
	var dfs func(n int, path []int) []int
	dfs = func(n int, path []int) []int {
		if n == issue {
			return path
		}
		if c.truncated[n] {
			// n's blocked-by list is incomplete; a hidden edge could reach
			// issue, so a "no cycle" answer here can't be trusted.
			verifiable = false
		}
		for _, m := range c.adj[n] {
			if !visited[m] {
				visited[m] = true
				if p := dfs(m, append(path, m)); p != nil {
					return p
				}
			}
		}
		return nil
	}
	p := dfs(blocker, []int{blocker})
	if p == nil {
		return CycleCheck{Verifiable: verifiable}
	}
	// p is blocker -> ... -> issue; the new edge closes issue -> blocker.
	// A found cycle is definite regardless of truncation elsewhere.
	return CycleCheck{Cycle: append([]int{issue}, p...), Verifiable: true}
}

// truncatedBlockers indexes issues whose blocked-by connection was capped.
func truncatedBlockers(issues []Issue) map[int]bool {
	out := make(map[int]bool)
	for _, i := range issues {
		if i.BlockedByTotal > len(i.BlockedBy) {
			out[i.Number] = true
		}
	}
	return out
}

// blockedByGraph builds the adjacency map of blocked-by edges between open
// issues in the set. Edges to closed or unknown issues are non-blocking and
// dropped.
func blockedByGraph(issues []Issue) map[int][]int {
	open := make(map[int]bool, len(issues))
	for _, i := range issues {
		if i.IsOpen() {
			open[i.Number] = true
		}
	}
	adj := make(map[int][]int)
	for _, i := range issues {
		if !open[i.Number] {
			continue
		}
		for _, b := range i.BlockedBy {
			if b.IsOpen() && open[b.Number] {
				adj[i.Number] = append(adj[i.Number], b.Number)
			}
		}
	}
	return adj
}

// canonical rotates a cycle so its smallest member comes first and appends
// the closing member, e.g. [4 5 3] -> [3 4 5 3].
func canonical(cyc []int) []int {
	min := 0
	for i, n := range cyc {
		if n < cyc[min] {
			min = i
		}
	}
	out := make([]int, 0, len(cyc)+1)
	out = append(out, cyc[min:]...)
	out = append(out, cyc[:min]...)
	return append(out, out[0])
}

func cycleKey(cyc []int) string {
	var b strings.Builder
	for _, n := range cyc {
		b.WriteByte('/')
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

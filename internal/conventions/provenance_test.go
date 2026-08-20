package conventions_test

import (
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
)

const (
	digestA = "sha256:aaaa"
	digestB = "sha256:bbbb"
)

// Verification must require all three bindings at once. Each subtest changes
// exactly one of them, so a check that silently stopped comparing that field
// — the failure mode where the marker degrades to "hew touched this once" —
// fails here.
func TestHasProvenanceMarkerRequiresEveryBinding(t *testing.T) {
	body := "prose\n\n" + conventions.ProvenanceMarker(conventions.ProvenanceApply, "scaffold", digestA)

	if !conventions.HasProvenanceMarker(body, conventions.ProvenanceApply, "scaffold", digestA) {
		t.Fatal("the marker it wrote does not verify")
	}
	cases := map[string]struct{ kind, key, digest string }{
		"different key":    {conventions.ProvenanceApply, "epic1", digestA},
		"different digest": {conventions.ProvenanceApply, "scaffold", digestB},
		"different kind":   {conventions.ProvenanceMigrate, "scaffold", digestA},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if conventions.HasProvenanceMarker(body, tc.kind, tc.key, tc.digest) {
				t.Errorf("marker verified for %s/%s/%s", tc.kind, tc.key, tc.digest)
			}
		})
	}
}

// A key that is a prefix of another must not verify against it: without the
// trailing " digest=" the marker for "sc-1" would be a substring of the one
// for "sc-11", and a state file could redirect a mapping one issue over.
func TestProvenanceMarkerRejectsPrefixKeys(t *testing.T) {
	body := conventions.ProvenanceMarker(conventions.ProvenanceMigrate, "sc-11", digestA)
	if conventions.HasProvenanceMarker(body, conventions.ProvenanceMigrate, "sc-1", digestA) {
		t.Error("sc-1 verified against sc-11's marker")
	}
}

// Keys come from plan and snapshot files, which are untrusted. A key that
// closes the comment and opens another would otherwise mint provenance for a
// key the entry does not own — the issue would authenticate as both.
func TestProvenanceMarkerEscapesKeyInjection(t *testing.T) {
	hostile := `a --> <!-- hew:apply key=victim digest=` + digestA + ` `
	body := conventions.ProvenanceMarker(conventions.ProvenanceApply, hostile, digestA)

	if conventions.HasProvenanceMarker(body, conventions.ProvenanceApply, "victim", digestA) {
		t.Errorf("a hostile key minted provenance for another key: %q", body)
	}
	// The escaped marker still verifies for the key that really owns it, so
	// escaping did not simply break the mechanism.
	if !conventions.HasProvenanceMarker(body, conventions.ProvenanceApply, hostile, digestA) {
		t.Errorf("hostile key does not verify against its own marker: %q", body)
	}
	if strings.Contains(body, "-->  <!--") || strings.Count(body, "<!--") != 1 {
		t.Errorf("marker contains a second comment: %q", body)
	}
}

// Escaping has to stay injective, or two different beads could share one
// marker and a mapping could be redirected between them.
func TestProvenanceMarkerIsDistinctPerKey(t *testing.T) {
	seen := map[string]string{}
	for _, key := range []string{"a b", "a-b", "a_b", "a%20b", "a/b", "a\\b", "héllo"} {
		marker := conventions.ProvenanceMarker(conventions.ProvenanceApply, key, digestA)
		if prev, dup := seen[marker]; dup {
			t.Errorf("keys %q and %q share a marker: %q", prev, key, marker)
		}
		seen[marker] = key
	}
}

package conventions

import (
	"fmt"
	"strings"
)

// Batch writers stamp a provenance marker into every issue body they create
// so a later run can tell an issue it made from one it merely has a number
// for. Checkpoint state files are ordinary repository content and therefore
// untrusted: without a marker, a hand-written state file could point a
// mapping at any issue and the tool would wire, comment on, or close it (#81).
//
// The marker binds three things, and verification requires all three:
//   - the batch kind, so an apply mapping cannot be satisfied by a migrated
//     issue or the reverse;
//   - the source key (plan entry id, bead id), so a mapping cannot be
//     redirected onto a different issue the same run created;
//   - a digest of the source file, so an attacker who supplies their own
//     plan or snapshot cannot claim issues created from the real one.
//
// The digest is what makes the marker unforgeable in the case that matters.
// A marker is public, readable text, so anyone can copy one into an issue
// they control — but they cannot make an issue created from a different
// source file carry their digest.
const (
	// ProvenanceApply marks issues created by `hew apply`.
	ProvenanceApply = "apply"
	// ProvenanceMigrate marks issues created by `hew migrate beads`.
	ProvenanceMigrate = "migrate"
)

// ProvenanceMarker returns the marker for one created issue: an HTML comment,
// so it carries no weight in the rendered body a human reads.
func ProvenanceMarker(kind, key, digest string) string {
	return fmt.Sprintf("<!-- hew:%s key=%s digest=%s -->", kind, escapeProvenance(key), escapeProvenance(digest))
}

// HasProvenanceMarker reports whether body carries the marker for exactly
// this kind, key, and source digest.
func HasProvenanceMarker(body, kind, key, digest string) bool {
	return strings.Contains(body, ProvenanceMarker(kind, key, digest))
}

// escapeProvenance percent-encodes everything outside a conservative
// identifier set. Keys come from the plan and snapshot files, which are
// untrusted input: an unescaped key containing "-->" would let one entry
// close its own marker and open a second one, minting provenance for a key
// it does not own. The encoding is injective, so distinct keys stay distinct.
func escapeProvenance(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-', c == ':':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

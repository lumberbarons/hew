package cli

import (
	"testing"

	"github.com/lumberbarons/hew/internal/conformance"
	"github.com/lumberbarons/hew/internal/gh"
)

// The fake stands in for GitHub in every command test. Running the shared
// suite against it keeps it honest: a behavior the local store and the fake
// disagree on is a bug in one of them.
func TestFakeClientConforms(t *testing.T) {
	conformance.Run(t, func(*testing.T) gh.IssueStore { return newFake() })
}

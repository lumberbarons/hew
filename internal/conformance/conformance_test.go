package conformance_test

import (
	"path/filepath"
	"testing"

	"github.com/lumberbarons/hew/internal/conformance"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
)

// The local store is run here, beside the suite it is checked against, so the
// suite's own statements are covered; the fake runs it from internal/cli.
func TestLocalStoreConforms(t *testing.T) {
	conformance.Run(t, func(t *testing.T) gh.IssueStore {
		dir := filepath.Join(t.TempDir(), local.DirName)
		if err := local.Init(dir); err != nil {
			t.Fatal(err)
		}
		s, err := local.Open(dir, "me")
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

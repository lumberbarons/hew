package cli

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
)

// Both backends carry the same four keys, so a driver parses one schema
// whichever backend is active.
func TestConfigJSONHasOneFlatShapeOnBothBackends(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target ConfigTarget
		want   backendConfig
	}{
		{
			"github",
			ConfigTarget{Repo: gh.Repo{Owner: "o", Name: "r"}},
			backendConfig{Backend: "github", Repo: "o/r"},
		},
		{
			"local",
			ConfigTarget{Tracker: "/repo/.hew", Local: local.Config{Backend: local.BackendLocal, ID: "tracker-id"}},
			backendConfig{Backend: "local", Path: "/repo/.hew", ID: "tracker-id"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, out, _ := newApp(newFake())
			app.JSON = true
			if err := app.Config(tc.target); err != nil {
				t.Fatal(err)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
				t.Fatalf("%v:\n%s", err, out)
			}
			keys := make([]string, 0, len(obj))
			for key := range obj {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			if want := []string{"backend", "id", "path", "repo"}; !slices.Equal(keys, want) {
				t.Errorf("keys = %v, want %v", keys, want)
			}
			var got backendConfig
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("config = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Text is the human form of the same data: only fields the active backend
// actually has, so a GitHub checkout never prints an empty tracker path.
func TestConfigTextPrintsOnlyTheFieldsTheBackendHas(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target ConfigTarget
		want   string
	}{
		{
			"github",
			ConfigTarget{Repo: gh.Repo{Owner: "o", Name: "r"}},
			"backend: github\nrepo: o/r\n",
		},
		{
			"local",
			ConfigTarget{Tracker: "/repo/.hew", Local: local.Config{Backend: local.BackendLocal, ID: "tracker-id"}},
			"backend: local\npath: /repo/.hew\nid: tracker-id\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, out, _ := newApp(newFake())
			if err := app.Config(tc.target); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

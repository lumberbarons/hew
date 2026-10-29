package cli

import (
	"encoding/json"
	"slices"
	"testing"
)

// Both backends carry the same four keys, so a driver parses one schema
// whichever backend is active.
func TestConfigJSONHasOneFlatShapeOnBothBackends(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  BackendConfig
	}{
		{"github", BackendConfig{Backend: "github", Repo: "o/r"}},
		{"local", BackendConfig{Backend: "local", Path: "/repo/.hew", ID: "tracker-id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, out, _ := newApp(newFake())
			app.JSON = true
			if err := app.Config(tc.cfg); err != nil {
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
			var got BackendConfig
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != tc.cfg {
				t.Errorf("config = %+v, want %+v", got, tc.cfg)
			}
		})
	}
}

// Text is the human form of the same data: only fields the active backend
// actually has, so a GitHub checkout never prints an empty tracker path.
func TestConfigTextPrintsOnlyTheFieldsTheBackendHas(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  BackendConfig
		want string
	}{
		{
			"github",
			BackendConfig{Backend: "github", Repo: "o/r"},
			"backend: github\nrepo: o/r\n",
		},
		{
			"local",
			BackendConfig{Backend: "local", Path: "/repo/.hew", ID: "tracker-id"},
			"backend: local\npath: /repo/.hew\nid: tracker-id\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, out, _ := newApp(newFake())
			if err := app.Config(tc.cfg); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

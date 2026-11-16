// This file publishes the smallest runnable examples of the public API. They
// are compiled and executed by `go test`, so the documentation cannot drift
// from the package.
package consumer_test

import (
	"fmt"

	"github.com/lumberbarons/hew/tracker"
)

// ExampleComposeBody is the composition a consumer shares with `hew create`:
// structured sections in, one template body out, no tracker or network.
func ExampleComposeBody() {
	composed, err := tracker.ComposeBody(tracker.Body{
		Sections: tracker.Sections{
			Goal:     "ship it",
			DoneWhen: []string{"tests pass"},
		},
	})
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	fmt.Println(composed.Body)
	// Output:
	// ### Goal
	//
	// ship it
	//
	// ### Done when
	//
	// - [ ] tests pass
}

// ExampleCheckBody shows the advisory half of composition: unmarked code is a
// finding on the result, not a refusal.
func ExampleCheckBody() {
	check, err := tracker.CheckBody("see internal/cli/write.go")
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	for _, finding := range check.Findings {
		fmt.Println(finding.Kind, finding.Tokens)
	}
	// Output:
	// unmarked-code [internal/cli/write.go]
}

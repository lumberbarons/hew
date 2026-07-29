package main

import (
	"fmt"

	"github.com/tiktoken-go/tokenizer"
)

// encodingName is the tokenizer every number in this harness is measured
// with. DESIGN.md's prime-budget spike used tiktoken o200k_base, so this one
// does too — a comparison against that figure is only meaningful if both
// sides count the same way.
//
// Claude's tokenizer is not published. o200k_base is the documented stand-in;
// per the spike note, Claude typically counts slightly higher, so the absolute
// figures read a little low while the ratios between two outputs hold.
const encodingName = "o200k_base"

// counter turns text into a token count. The vocabulary is compiled into the
// tokenizer package, so counting needs no network and cannot drift between
// runs.
type counter struct{ codec tokenizer.Codec }

func newCounter() (*counter, error) {
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", encodingName, err)
	}
	return &counter{codec: codec}, nil
}

func (c *counter) count(s string) (int, error) {
	ids, _, err := c.codec.Encode(s)
	if err != nil {
		return 0, fmt.Errorf("tokenize with %s: %w", encodingName, err)
	}
	return len(ids), nil
}

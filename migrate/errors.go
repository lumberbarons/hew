package migrate

import "fmt"

// DuplicateKeyError reports two items sharing a checkpoint key. Keys identify
// an item in the checkpoint and in its provenance marker, so the run refuses
// before writing anything rather than letting the second item overwrite the
// first's mapping.
type DuplicateKeyError struct {
	Key string
	// ItemNoun names the source's records ("bead"); empty means "item".
	ItemNoun string
}

func (e *DuplicateKeyError) Error() string {
	noun := e.ItemNoun
	if noun == "" {
		noun = "item"
	}
	return fmt.Sprintf("%s %s appears more than once; keys identify items in the checkpoint and must be unique", noun, e.Key)
}

// ItemError reports a source item the engine cannot migrate because one of its
// own fields is invalid.
type ItemError struct {
	Key    string
	Field  string
	Reason string
}

func (e *ItemError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("migration item has an invalid %s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("migration item %q has an invalid %s: %s", e.Key, e.Field, e.Reason)
}

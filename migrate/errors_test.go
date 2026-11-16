package migrate

import (
	"strings"
	"testing"
)

func TestDuplicateKeyErrorNamesTheSourceNoun(t *testing.T) {
	err := (&DuplicateKeyError{Key: "sc-1", ItemNoun: "bead"}).Error()
	if !strings.Contains(err, "bead sc-1 appears more than once") {
		t.Errorf("error = %q, want the source noun and key", err)
	}
	if got := (&DuplicateKeyError{Key: "a"}).Error(); !strings.HasPrefix(got, "item a ") {
		t.Errorf("error = %q, want the generic item noun", got)
	}
}

func TestItemErrorNamesTheItemWhenItHasAKey(t *testing.T) {
	err := (&ItemError{Key: "sc-1", Field: "title", Reason: "must not be empty"}).Error()
	if !strings.Contains(err, `"sc-1"`) || !strings.Contains(err, "title") {
		t.Errorf("error = %q, want the item's key and field", err)
	}
	if got := (&ItemError{Field: "key", Reason: "must not be empty"}).Error(); !strings.Contains(got, "invalid key") {
		t.Errorf("error = %q, want the unnamed form to name the field", got)
	}
}

func TestWarningStringIsItsMessage(t *testing.T) {
	w := Warning{Kind: WarnWriteFailed, Key: "a", Issue: 7, Message: "comment on #7: boom"}
	if w.String() != w.Message {
		t.Errorf("String() = %q, want the message %q", w.String(), w.Message)
	}
}

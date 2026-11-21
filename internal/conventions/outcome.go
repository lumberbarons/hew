package conventions

import (
	"errors"
	"regexp"
	"strings"
)

// AppendOutcome adds a note to the first top-level ATX Outcome
// section, creating one if needed. It inserts text without reformatting the
// rest of the body. Notes must leave the surrounding Markdown blocks intact.
func AppendOutcome(body, note string) (string, error) {
	if err := validateOutcomeNote(note); err != nil {
		return "", err
	}
	const heading = "### Outcome"
	var offset, end int
	var blocks outcomeBlocks
	var found bool
	for raw := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		at := offset
		offset += len(raw) + 1
		if found && !blocks.open() && strings.HasPrefix(line, "<!-- hew:") {
			return insertOutcome(body, end, note), nil
		}
		if !blocks.consume(line, trimmed) {
			if level, title := outcomeHeading(trimmed); level > 0 {
				if found && level <= 3 {
					return insertOutcome(body, end, note), nil
				}
				found = found || (level == 3 && strings.EqualFold(title, "Outcome"))
			}
		}
		if trimmed != "" {
			end = at + len(line)
		}
	}
	if blocks.open() {
		return "", errors.New("close the Markdown code fence or comment in the issue body first")
	}
	if !found {
		note = heading + "\n\n" + note
	}
	return insertOutcome(body, end, note), nil
}

func validateOutcomeNote(note string) error {
	var blocks outcomeBlocks
	for line := range strings.SplitSeq(note, "\n") {
		trimmed := strings.TrimSpace(line)
		if !blocks.consume(line, trimmed) {
			if level, _ := outcomeHeading(trimmed); level > 0 && level <= 3 {
				return errors.New("use level-four or deeper headings within an outcome note")
			}
		}
	}
	if blocks.open() {
		return errors.New("close the Markdown code fence or comment in the outcome note")
	}
	return nil
}

func outcomeHeading(line string) (level int, title string) {
	title, ok := headingTitle(line)
	if !ok {
		return 0, ""
	}
	return len(line) - len(strings.TrimLeft(line, "#")), title
}

var outcomeListMarker = regexp.MustCompile(`^ {0,3}(?:[-+*]|[0-9]{1,9}[.)]) {1,4}`)

type outcomeBlocks struct {
	fence      string
	comment    bool
	listIndent int
}

func (b outcomeBlocks) open() bool {
	return b.fence != "" || b.comment
}

// consume reports whether the line is inside a code block, comment or list.
func (b *outcomeBlocks) consume(line, trimmed string) bool {
	if b.listIndent > 0 {
		if trimmed == "" || len(line)-len(strings.TrimLeft(line, " ")) >= b.listIndent {
			return true
		}
		b.listIndent = 0
	}
	switch {
	case b.comment:
		b.comment = !strings.Contains(trimmed, "-->")
	case b.fence != "":
		if strings.HasPrefix(trimmed, b.fence) && strings.Trim(trimmed, b.fence[:1]) == "" {
			b.fence = ""
		}
	case strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t"):
	case strings.HasPrefix(trimmed, "<!--"):
		b.comment = !strings.Contains(trimmed, "-->")
	default:
		b.listIndent = len(outcomeListMarker.FindString(line))
		b.fence = openingFence(trimmed)
		return b.fence != "" || b.listIndent > 0
	}
	return true
}

func insertOutcome(body string, at int, note string) string {
	if at == 0 {
		return note + body
	}
	separator := "\n\n"
	if strings.Contains(body, "\r\n") {
		separator = "\r\n\r\n"
		note = strings.ReplaceAll(strings.ReplaceAll(note, "\r\n", "\n"), "\n", "\r\n")
	}
	return body[:at] + separator + note + body[at:]
}

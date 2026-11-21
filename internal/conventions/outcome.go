package conventions

import (
	"errors"
	"strings"
)

// AppendOutcome adds a note to the first level-three Outcome section, creating
// one if needed. It inserts text without reformatting the rest of the body.
func AppendOutcome(body, note string) (string, error) {
	const heading = "### Outcome"
	var offset, end int
	var fence string
	var found, comment bool
	for raw := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		at := offset
		offset += len(raw) + 1
		switch {
		case comment:
			comment = !strings.Contains(trimmed, "-->")
		case fence != "":
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
		case strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t"):
			// Indented code cannot start or end a section.
		case strings.HasPrefix(trimmed, "<!--"):
			if found && strings.HasPrefix(trimmed, "<!-- hew:") {
				return insertOutcome(body, end, note), nil
			}
			comment = !strings.Contains(trimmed, "-->")
		default:
			fence = openingFence(trimmed)
			if title, ok := headingTitle(trimmed); ok {
				level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
				if found && level <= 3 {
					return insertOutcome(body, end, note), nil
				}
				found = level == 3 && strings.EqualFold(title, "Outcome") || found
			}
		}
		if trimmed != "" {
			end = at + len(line)
		}
	}
	if fence != "" || comment {
		return "", errors.New("close the Markdown code fence or comment in the issue body first")
	}
	if !found {
		note = heading + "\n\n" + note
	}
	return insertOutcome(body, end, note), nil
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

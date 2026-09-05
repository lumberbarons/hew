package render

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// The palette comes from the landing page (site/index.html), so the terminal
// and the page read as one product: amber issue numbers, green priorities,
// dim secondary text. Truecolor SGR, emitted only around the semantic span —
// the codes are zero-width on any terminal, so column alignment is computed
// on the plain strings exactly as before.
const (
	sgrAmber = "\x1b[38;2;242;169;59m"  // #f2a93b — issue numbers
	sgrGreen = "\x1b[38;2;95;214;139m"  // #5fd68b — priorities
	sgrDim   = "\x1b[38;2;110;125;113m" // #6e7d71 — secondary text
	sgrReset = "\x1b[0m"
)

// Style carries whether text renderers wrap their semantic spans in ANSI.
// The zero value renders plain — every existing golden test, agent pipe, and
// --json stream stays byte-identical.
type Style struct {
	on bool
}

// ColorEnabled decides whether output to w carries color. FORCE_COLOR=1
// opts back in unconditionally (tests, pagers that can render it, pipes);
// otherwise color needs the standard opt-out contract unset — NO_COLOR
// unset, TERM not "dumb" — and w to be a real terminal.
func ColorEnabled(w io.Writer) bool {
	if os.Getenv("FORCE_COLOR") == "1" {
		return true
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// StyleFor converts the precomputed color decision into a Style. The
// decision is made once, in main, against the real stdout; the renderers
// only ever see the boolean.
func StyleFor(on bool) Style { return Style{on: on} }

// num wraps "#n" in amber.
func (s Style) num(n int) string {
	out := "#" + strconv.Itoa(n)
	if !s.on {
		return out
	}
	return sgrAmber + out + sgrReset
}

// numPadded renders the right-aligned "#n" column with the padding inside
// the amber span, so the codes never contribute to the column width.
func (s Style) numPadded(n, width int) string {
	if !s.on {
		return fmt.Sprintf("#%-*d", width, n)
	}
	return sgrAmber + fmt.Sprintf("#%-*d", width, n) + sgrReset
}

// metaPadded renders the padded meta column ("P2 enhancement (tests)")
// with the leading priority token green and the rest untouched.
func (s Style) metaPadded(m string, width int) string {
	m = fmt.Sprintf("%-*s", width, m)
	if !s.on {
		return m
	}
	if i := strings.IndexByte(m, ' '); i > 0 {
		return sgrGreen + m[:i] + sgrReset + m[i:]
	}
	return sgrGreen + m + sgrReset
}

// dim wraps secondary text in the dimmed palette; empty text is returned
// untouched so callers can pass optional annotations blindly.
func (s Style) dim(t string) string {
	if t == "" || !s.on {
		return t
	}
	return sgrDim + t + sgrReset
}

// refNum wraps one "#n (closed)" reference's number in amber; the rest of
// the reference is plain.
func (s Style) refNum(n int, closed bool) string {
	out := s.num(n)
	if closed {
		out += " (closed)"
	}
	return out
}

package patch

import (
	"fmt"
	"strings"
)

// ParseError is a patch that could not be read: a malformed header, an
// unsupported construct, a hunk body that disagrees with its own line counts.
//
// It carries the offending patch line number and the line itself, because the
// caller holds the patch text and a quoted line is the fastest way for them to
// find the problem.
type ParseError struct {
	// Line is the 1-based line number within the patch, zero when the problem
	// is with the patch as a whole.
	Line int

	// Text is the offending patch line, if there is one.
	Text string

	// Reason states what is wrong, in the caller's terms.
	Reason string

	// Hint names what to do instead, when there is a clear answer.
	Hint string
}

func (e *ParseError) Error() string {
	var b strings.Builder
	b.WriteString("patch is not readable: ")
	b.WriteString(e.Reason)
	if e.Line > 0 {
		fmt.Fprintf(&b, "\nat patch line %d: %s", e.Line, quote(e.Text))
	}
	if e.Hint != "" {
		b.WriteString("\n")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// ApplyError is a patch that reads correctly but does not fit the file it was
// given.
//
// Expected and Found are the crux: the caller diffed against something, and
// the only useful reply is what the file says now where they believed it said
// something else.
type ApplyError struct {
	Path string

	// HunkIndex is the 1-based position of the failing hunk within its file
	// section, zero when the failure is about the file as a whole.
	HunkIndex int

	// Header is the failing hunk's verbatim "@@ ..." line.
	Header string

	// AtLine is the 1-based line the hunk was expected at.
	AtLine int

	// Expected is the context and removed lines the hunk requires.
	Expected []string

	// Found is what the file actually has there.
	Found []string

	// Reason replaces the expected/found comparison when the failure is not a
	// context mismatch.
	Reason string
}

func (e *ApplyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "patch does not apply to %s", e.Path)

	if e.Reason != "" {
		fmt.Fprintf(&b, ": %s", e.Reason)
		return b.String()
	}

	fmt.Fprintf(&b, "\nhunk #%d %s expects at line %d:\n%s",
		e.HunkIndex, strings.TrimSpace(e.Header), e.AtLine, indent(e.Expected))
	if len(e.Found) == 0 {
		b.WriteString("\nbut the file ends before that")
	} else {
		fmt.Fprintf(&b, "\nbut found:\n%s", indent(e.Found))
	}
	return b.String()
}

// indent renders lines as a quoted block that cannot be confused with the
// surrounding message, whatever the lines themselves contain.
func indent(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, "  | "+l)
	}
	return strings.Join(out, "\n")
}

func quote(s string) string {
	return `"` + strings.TrimRight(s, "\r") + `"`
}

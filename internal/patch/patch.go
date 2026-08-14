// Package patch parses and applies unified diffs.
//
// It exists so a caller can describe an edit by what changed rather than by
// restating a whole file. The scope is deliberately narrow: text files,
// add/modify/delete, no renames, no modes, no binary payloads. Anything
// outside that is rejected with a message naming the construct, because a
// caller that hits the limit needs to know to send full content instead.
//
// Applying is strict about content and lenient about position. Context and
// removed lines must match byte for byte - no fuzz, no whitespace tolerance -
// but a hunk may be found at an offset from the line number in its @@ header,
// the way git apply locates one. That combination is what makes a patch safe
// to accept from a caller whose copy of the file is a few lines out of date:
// the edit lands where its surroundings say it belongs, or not at all.
//
// The "\ No newline at end of file" marker is authoritative on the new side -
// output ends without a trailing newline only when the last new-side line
// carries it - and advisory on the old side, where its absence never fails a
// patch. Not every tool emits it, and a missing final newline in the base is
// not a reason to reject an otherwise well-formed edit.
package patch

import (
	"fmt"
	"strconv"
	"strings"
)

// File is one file's section of a patch.
type File struct {
	// OldPath is the path the hunks apply to, empty when the file is being
	// created.
	OldPath string

	// NewPath is the path the result is written to, empty when the file is
	// being deleted.
	NewPath string

	Hunks []Hunk
}

// Created reports whether this section creates a file that does not exist yet.
func (f File) Created() bool { return f.OldPath == "" }

// Deleted reports whether this section removes an existing file.
func (f File) Deleted() bool { return f.NewPath == "" }

// Path is the file the section is about, whichever side names it.
func (f File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Hunk is one contiguous region of change within a file.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int

	// Header is the verbatim "@@ ..." line, quoted back in error messages so
	// a caller can find the hunk in the patch it sent.
	Header string

	Lines []Line
}

// Line is one line of a hunk body.
type Line struct {
	// Op is ' ' for context, '-' for removed, '+' for added.
	Op byte

	// Text is the line without its trailing newline.
	Text string

	// NoNewline records that "\ No newline at end of file" followed this line.
	NoNewline bool
}

// Change is one file's resulting state after a patch is applied.
type Change struct {
	Path    string
	Deleted bool
	Content string
}

// Apply applies files to current, keyed by path, and returns one Change per
// file the patch touches. current is not modified.
//
// It is all or nothing: if any hunk fails, no Change is returned at all, so a
// caller never has to reason about a partially applied patch.
func Apply(files []File, current map[string]string) ([]Change, error) {
	changes := make([]Change, 0, len(files))

	for _, f := range files {
		base, exists := current[f.Path()]

		switch {
		case f.Created() && exists:
			return nil, &ApplyError{
				Path:   f.Path(),
				Reason: "the patch creates this file, but it already exists",
			}
		case !f.Created() && !exists:
			return nil, &ApplyError{
				Path:   f.Path(),
				Reason: "the patch modifies this file, but it does not exist",
			}
		case f.Deleted():
			changes = append(changes, Change{Path: f.Path(), Deleted: true})
			continue
		}

		content, err := applyFile(f, base)
		if err != nil {
			return nil, err
		}
		changes = append(changes, Change{Path: f.Path(), Content: content})
	}

	return changes, nil
}

// applyFile applies one file section's hunks to base.
func applyFile(f File, base string) (string, error) {
	lines, trailingNewline := splitLines(base)

	// offset tracks how far the file has drifted from what the patch's line
	// numbers assume, so each hunk starts its search from where the previous
	// ones actually landed rather than from where the patch guessed.
	var offset int

	// minStart keeps hunks from being matched out of order: a later hunk can
	// never land before an earlier one finished.
	var minStart int

	var out []string
	var consumed int
	endsWithoutNewline := false

	for i, h := range f.Hunks {
		want := hunkOldLines(h)

		start, err := locate(lines, want, h.OldStart-1+offset, minStart)
		if err != nil {
			err.Path = f.Path()
			err.HunkIndex = i + 1
			err.Header = h.Header
			return "", err
		}

		out = append(out, lines[consumed:start]...)

		for _, l := range h.Lines {
			if l.Op == '-' {
				continue
			}
			out = append(out, l.Text)
			endsWithoutNewline = l.NoNewline
		}

		consumed = start + len(want)
		minStart = consumed
		offset = start - (h.OldStart - 1)
	}

	out = append(out, lines[consumed:]...)

	if len(out) == 0 {
		return "", nil
	}
	// A hunk that did not reach the end of the file leaves the base file's own
	// ending in place; only a hunk that rewrote the last line can change it.
	if consumed < len(lines) {
		endsWithoutNewline = !trailingNewline
	}

	result := strings.Join(out, "\n")
	if !endsWithoutNewline {
		result += "\n"
	}
	return result, nil
}

// locate finds where want matches in lines, preferring anchor and searching
// outward from it. Matches before floor are rejected so hunks stay in order.
func locate(lines, want []string, anchor, floor int) (int, *ApplyError) {
	if anchor < floor {
		anchor = floor
	}
	maxStart := len(lines) - len(want)

	// A pure insertion has nothing to match against, so it goes exactly where
	// the patch says, adjusted for drift.
	if len(want) == 0 {
		if anchor > len(lines) {
			anchor = len(lines)
		}
		return anchor, nil
	}

	for d := 0; d <= len(lines); d++ {
		for _, at := range [2]int{anchor + d, anchor - d} {
			if at < floor || at > maxStart {
				continue
			}
			if matchAt(lines, want, at) {
				return at, nil
			}
			if d == 0 {
				break // anchor+0 and anchor-0 are the same position
			}
		}
	}

	return 0, mismatchAt(lines, want, anchor)
}

// mismatchAt builds the error for a hunk that matched nowhere, describing the
// anchor position - the one the caller believed it was editing.
func mismatchAt(lines, want []string, anchor int) *ApplyError {
	found := make([]string, 0, len(want))
	for i := anchor; i < anchor+len(want) && i < len(lines); i++ {
		found = append(found, lines[i])
	}
	return &ApplyError{
		AtLine:   anchor + 1,
		Expected: want,
		Found:    found,
	}
}

func matchAt(lines, want []string, at int) bool {
	for i, w := range want {
		if lines[at+i] != w {
			return false
		}
	}
	return true
}

// hunkOldLines is the sequence a hunk expects to find in the file: its context
// and removed lines, in order.
func hunkOldLines(h Hunk) []string {
	out := make([]string, 0, len(h.Lines))
	for _, l := range h.Lines {
		if l.Op == ' ' || l.Op == '-' {
			out = append(out, l.Text)
		}
	}
	return out
}

// splitLines splits content into lines without their terminators, and reports
// whether the content ended with a newline. An empty file is zero lines.
func splitLines(content string) (lines []string, trailingNewline bool) {
	if content == "" {
		return nil, true
	}
	trailingNewline = strings.HasSuffix(content, "\n")
	if trailingNewline {
		content = content[:len(content)-1]
	}
	return strings.Split(content, "\n"), trailingNewline
}

// AlreadyApplied reports whether the patch's result is already present in
// current - the likeliest reason a well-formed patch fails to apply, and worth
// saying out loud rather than leaving the caller to compare context by hand.
func AlreadyApplied(files []File, current map[string]string) bool {
	for _, f := range files {
		base, exists := current[f.Path()]
		if f.Deleted() {
			if exists {
				return false
			}
			continue
		}
		if f.Created() && !exists {
			return false
		}
		if !hunksPresent(f, base) {
			return false
		}
	}
	return len(files) > 0
}

// hunksPresent reports whether every hunk's new side already appears in base.
func hunksPresent(f File, base string) bool {
	lines, _ := splitLines(base)
	for _, h := range f.Hunks {
		want := make([]string, 0, len(h.Lines))
		for _, l := range h.Lines {
			if l.Op == ' ' || l.Op == '+' {
				want = append(want, l.Text)
			}
		}
		if len(want) == 0 {
			continue
		}
		if _, err := locate(lines, want, h.NewStart-1, 0); err != nil {
			return false
		}
	}
	return true
}

// Paths returns every path the patch touches, in order.
func Paths(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path())
	}
	return out
}

// atoiHunk parses a hunk header's line count, defaulting to 1 for the form
// that omits it ("@@ -3 +3 @@" means one line).
func atoiHunk(s string) (start, count int, err error) {
	count = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		count, err = strconv.Atoi(s[i+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("bad line count %q", s[i+1:])
		}
		s = s[:i]
	}
	start, err = strconv.Atoi(s)
	if err != nil {
		return 0, 0, fmt.Errorf("bad line number %q", s)
	}
	return start, count, nil
}

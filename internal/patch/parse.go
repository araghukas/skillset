package patch

import (
	"fmt"
	"strings"
)

// devNull is the path both diff sides use to mean "this file does not exist on
// this side".
const devNull = "/dev/null"

// Parse reads a unified diff into per-file sections.
//
// Anything before the first file section is skipped, so a patch pasted with a
// sentence of explanation in front of it still parses. Within a section,
// "index" and "similarity index" lines are ignored; renames, copies, mode
// changes and binary payloads are rejected, since expressing them as text
// hunks is not possible and silently dropping them would lose the change.
func Parse(text string) ([]File, error) {
	lines := strings.Split(text, "\n")
	// The split of a newline-terminated patch ends in an empty element that is
	// not a line of it, and a hunk body would otherwise count it as context.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	p := &parser{lines: lines}

	var files []File
	for {
		f, ok, err := p.nextFile()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		files = append(files, f)
	}

	if len(files) == 0 {
		return nil, &ParseError{
			Reason: "it contains no file diffs",
			Hint:   `Expected sections starting with "diff --git" or "--- ".`,
		}
	}
	return files, nil
}

type parser struct {
	lines []string
	i     int
}

func (p *parser) done() bool      { return p.i >= len(p.lines) }
func (p *parser) peek() string    { return p.lines[p.i] }
func (p *parser) lineNo() int     { return p.i + 1 }
func (p *parser) advance() string { l := p.lines[p.i]; p.i++; return l }

func (p *parser) errf(reason string, args ...any) *ParseError {
	e := &ParseError{Reason: fmt.Sprintf(reason, args...)}
	if !p.done() {
		e.Line = p.lineNo()
		e.Text = p.peek()
	}
	return e
}

// startsSection reports whether a line begins a new file's diff, which is also
// how the parser knows the previous file's hunks have ended.
func startsSection(line string) bool {
	return strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "--- ")
}

// nextFile reads one file section, skipping any preamble before it. ok is
// false once no more sections remain.
func (p *parser) nextFile() (f File, ok bool, err error) {
	for !p.done() && !startsSection(p.peek()) {
		p.advance()
	}
	if p.done() {
		return File{}, false, nil
	}

	created, deleted, perr := p.fileHeader()
	if perr != nil {
		return File{}, false, perr
	}

	oldPath, newPath, perr := p.pathLines()
	if perr != nil {
		return File{}, false, perr
	}
	if created {
		oldPath = ""
	}
	if deleted {
		newPath = ""
	}
	if oldPath == "" && newPath == "" {
		return File{}, false, p.errf("a file diff names no file on either side")
	}

	hunks, perr := p.hunks(oldPath == "" || newPath == "")
	if perr != nil {
		return File{}, false, perr
	}
	if len(hunks) == 0 && newPath != "" {
		return File{}, false, &ParseError{
			Reason: fmt.Sprintf("the section for %q has no hunks, so it describes no change", oldPath),
		}
	}

	return File{OldPath: oldPath, NewPath: newPath, Hunks: hunks}, true, nil
}

// fileHeader consumes a "diff --git" preamble, if there is one, and reports
// what its extended header lines said about the file's existence.
func (p *parser) fileHeader() (created, deleted bool, err *ParseError) {
	if !strings.HasPrefix(p.peek(), "diff --git ") {
		return false, false, nil
	}
	p.advance()

	var sawOldMode bool
	for !p.done() {
		line := p.peek()
		switch {
		case strings.HasPrefix(line, "new file mode"):
			created = true
		case strings.HasPrefix(line, "deleted file mode"):
			deleted = true
		case strings.HasPrefix(line, "rename from"), strings.HasPrefix(line, "rename to"):
			return false, false, p.unsupported("a rename")
		case strings.HasPrefix(line, "copy from"), strings.HasPrefix(line, "copy to"):
			return false, false, p.unsupported("a copy")
		case strings.HasPrefix(line, "old mode"):
			sawOldMode = true
		case strings.HasPrefix(line, "new mode"):
			if sawOldMode {
				return false, false, p.unsupported("a file mode change")
			}
		case strings.HasPrefix(line, "GIT binary patch"), strings.HasPrefix(line, "Binary files "):
			return false, false, p.unsupported("a binary payload")
		case strings.HasPrefix(line, "index "), strings.HasPrefix(line, "similarity index "),
			strings.HasPrefix(line, "dissimilarity index "):
			// Advisory metadata; nothing here depends on it.
		default:
			return created, deleted, nil
		}
		p.advance()
	}
	return created, deleted, nil
}

func (p *parser) unsupported(construct string) *ParseError {
	return p.errf("it uses %s, which this server cannot apply", construct)
}

// pathLines consumes the "---" and "+++" pair.
func (p *parser) pathLines() (oldPath, newPath string, err *ParseError) {
	if p.done() || !strings.HasPrefix(p.peek(), "--- ") {
		return "", "", p.errf(`expected a "--- " line naming the file being changed`)
	}
	oldPath = diffPath(strings.TrimPrefix(p.advance(), "--- "), "a/")

	if p.done() || !strings.HasPrefix(p.peek(), "+++ ") {
		return "", "", p.errf(`expected a "+++ " line after the "--- " line`)
	}
	newPath = diffPath(strings.TrimPrefix(p.advance(), "+++ "), "b/")

	return oldPath, newPath, nil
}

// diffPath cleans one side of a file header: drops the timestamp field plain
// diff appends, strips the one-letter prefix git adds, and turns /dev/null
// into the empty path.
func diffPath(field, prefix string) string {
	if i := strings.IndexByte(field, '\t'); i >= 0 {
		field = field[:i]
	}
	field = strings.TrimSpace(strings.TrimRight(field, "\r"))
	if field == devNull {
		return ""
	}
	return strings.TrimPrefix(field, prefix)
}

// hunks reads every hunk up to the next file section. wholeFile relaxes the
// ordering rule, which a creation or deletion has no use for.
func (p *parser) hunks(wholeFile bool) ([]Hunk, *ParseError) {
	var hunks []Hunk
	var prevEnd int

	for !p.done() && strings.HasPrefix(p.peek(), "@@") {
		h, err := p.hunk()
		if err != nil {
			return nil, err
		}
		if !wholeFile && h.OldStart < prevEnd {
			return nil, &ParseError{
				Reason: fmt.Sprintf("hunk %q overlaps or precedes the hunk before it; hunks must be in ascending order",
					strings.TrimSpace(h.Header)),
			}
		}
		prevEnd = h.OldStart + h.OldLines
		hunks = append(hunks, h)
	}
	return hunks, nil
}

// hunk reads one "@@" header and the body its counts describe.
func (p *parser) hunk() (Hunk, *ParseError) {
	header := p.peek()
	h := Hunk{Header: header}

	oldField, newField, ok := hunkFields(header)
	if !ok {
		return Hunk{}, p.errf(`malformed hunk header (want "@@ -old,count +new,count @@")`)
	}

	var err error
	if h.OldStart, h.OldLines, err = atoiHunk(oldField); err != nil {
		return Hunk{}, p.errf("malformed hunk header: %s", err)
	}
	if h.NewStart, h.NewLines, err = atoiHunk(newField); err != nil {
		return Hunk{}, p.errf("malformed hunk header: %s", err)
	}
	p.advance()

	var oldSeen, newSeen int
	for !p.done() && (oldSeen < h.OldLines || newSeen < h.NewLines) {
		line := p.advance()

		// Trailing whitespace is routinely stripped in transit, which turns a
		// context line for a blank line into an empty string.
		if line == "" {
			line = " "
		}

		switch line[0] {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		case '\\':
			if n := len(h.Lines); n > 0 {
				h.Lines[n-1].NoNewline = true
			}
			continue
		default:
			p.i-- // report the offending line, not the one after it
			return Hunk{}, p.errf("hunk body line must start with a space, '-' or '+'")
		}

		h.Lines = append(h.Lines, Line{Op: line[0], Text: strings.TrimRight(line[1:], "\r")})
	}

	if oldSeen != h.OldLines || newSeen != h.NewLines {
		return Hunk{}, &ParseError{
			Reason: fmt.Sprintf("hunk %q claims %d old and %d new lines but its body has %d and %d",
				strings.TrimSpace(header), h.OldLines, h.NewLines, oldSeen, newSeen),
			Hint: "Recompute the patch with a diff tool rather than editing hunk headers by hand.",
		}
	}

	// A trailing "\ No newline" marker sits after the counted body.
	if !p.done() && strings.HasPrefix(p.peek(), "\\") {
		if n := len(h.Lines); n > 0 {
			h.Lines[n-1].NoNewline = true
		}
		p.advance()
	}

	return h, nil
}

// hunkFields pulls the "-old,count" and "+new,count" fields out of a hunk
// header, ignoring the section heading git appends after the closing "@@".
func hunkFields(header string) (oldField, newField string, ok bool) {
	rest, ok := strings.CutPrefix(header, "@@ ")
	if !ok {
		return "", "", false
	}
	end := strings.Index(rest, "@@")
	if end < 0 {
		return "", "", false
	}
	fields := strings.Fields(rest[:end])
	if len(fields) != 2 {
		return "", "", false
	}
	oldField, ok = strings.CutPrefix(fields[0], "-")
	if !ok {
		return "", "", false
	}
	newField, ok = strings.CutPrefix(fields[1], "+")
	if !ok {
		return "", "", false
	}
	return oldField, newField, true
}

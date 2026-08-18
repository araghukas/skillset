package patch

import (
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		patch string

		wantFiles int
		wantOld   string
		wantNew   string
		wantHunks int
		wantErr   string
	}{
		{
			name: "plain diff -u output",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`,
			wantFiles: 1, wantOld: "SKILL.md", wantNew: "SKILL.md", wantHunks: 1,
		},
		{
			name: "git prefixes are stripped",
			patch: `diff --git a/SKILL.md b/SKILL.md
index 83db48f..bf269f4 100644
--- a/SKILL.md
+++ b/SKILL.md
@@ -1 +1 @@
-one
+ONE
`,
			wantFiles: 1, wantOld: "SKILL.md", wantNew: "SKILL.md", wantHunks: 1,
		},
		{
			name: "timestamps after a tab are dropped",
			patch: "--- orig/SKILL.md\t2026-08-13 10:00:00\n" +
				"+++ new/SKILL.md\t2026-08-13 10:01:00\n" +
				"@@ -1 +1 @@\n-one\n+ONE\n",
			wantFiles: 1, wantOld: "orig/SKILL.md", wantNew: "new/SKILL.md", wantHunks: 1,
		},
		{
			name: "prose before the diff is skipped",
			patch: `Here is the change I want to make:

--- SKILL.md
+++ SKILL.md
@@ -1 +1 @@
-one
+ONE
`,
			wantFiles: 1, wantOld: "SKILL.md", wantNew: "SKILL.md", wantHunks: 1,
		},
		{
			name: "multiple files",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1 +1 @@
-one
+ONE
--- references/old.txt
+++ references/old.txt
@@ -1 +1 @@
-a
+b
`,
			wantFiles: 2,
		},
		{
			name: "multiple hunks in one file",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,2 +1,2 @@
-one
+ONE
 two
@@ -8,2 +8,2 @@
-eight
+EIGHT
 nine
`,
			wantFiles: 1, wantHunks: 2,
		},
		{
			name: "creation",
			patch: `--- /dev/null
+++ references/new.md
@@ -0,0 +1,2 @@
+alpha
+beta
`,
			wantFiles: 1, wantOld: "", wantNew: "references/new.md", wantHunks: 1,
		},
		{
			name: "deletion via /dev/null",
			patch: `--- references/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-a
-b
`,
			wantFiles: 1, wantOld: "references/old.txt", wantNew: "",
		},
		{
			name: "deletion via file mode header",
			patch: `diff --git a/references/old.txt b/references/old.txt
deleted file mode 100644
index 83db48f..0000000
--- a/references/old.txt
+++ b/references/old.txt
@@ -1 +0,0 @@
-a
`,
			wantFiles: 1, wantOld: "references/old.txt", wantNew: "",
		},
		{
			name: "new file mode header",
			patch: `diff --git a/references/new.md b/references/new.md
new file mode 100644
--- a/references/new.md
+++ b/references/new.md
@@ -0,0 +1 @@
+alpha
`,
			wantFiles: 1, wantOld: "", wantNew: "references/new.md",
		},
		{
			name:    "empty patch",
			patch:   "",
			wantErr: "no file diffs",
		},
		{
			name:    "prose only",
			patch:   "I would change the second line to say TWO.\n",
			wantErr: "no file diffs",
		},
		{
			name: "line counts disagree with the body",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,5 +1,5 @@
 one
-two
+TWO
`,
			wantErr: "claims 5 old and 5 new lines but its body has 2 and 2",
		},
		{
			name: "malformed hunk header",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1 +@@
-one
+ONE
`,
			wantErr: "malformed hunk header",
		},
		{
			name: "rename",
			patch: `diff --git a/a.md b/b.md
similarity index 100%
rename from a.md
rename to b.md
`,
			wantErr: "a rename",
		},
		{
			name: "copy",
			patch: `diff --git a/a.md b/b.md
copy from a.md
copy to b.md
`,
			wantErr: "a copy",
		},
		{
			name: "mode change",
			patch: `diff --git a/scripts/run.sh b/scripts/run.sh
old mode 100644
new mode 100755
`,
			wantErr: "a file mode change",
		},
		{
			name: "binary payload",
			patch: `diff --git a/assets/logo.png b/assets/logo.png
GIT binary patch
literal 12
`,
			wantErr: "a binary payload",
		},
		{
			name: "section with no hunks",
			patch: `--- SKILL.md
+++ SKILL.md
`,
			wantErr: "has no hunks",
		},
		{
			name: "missing +++ line",
			patch: `--- SKILL.md
@@ -1 +1 @@
-one
+ONE
`,
			wantErr: `expected a "+++ " line`,
		},
		{
			name: "bad body line prefix",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
!two
+TWO
`,
			wantErr: "must start with a space",
		},
		{
			name: "hunks out of order",
			patch: `--- SKILL.md
+++ SKILL.md
@@ -8,1 +8,1 @@
-eight
+EIGHT
@@ -1,1 +1,1 @@
-one
+ONE
`,
			wantErr: "ascending order",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := Parse(tt.patch)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse succeeded, want error containing %q", tt.wantErr)
				}
				var perr *ParseError
				if !errors.As(err, &perr) {
					t.Errorf("error is %T, want *ParseError", err)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if tt.wantFiles > 0 && len(files) != tt.wantFiles {
				t.Fatalf("got %d files, want %d", len(files), tt.wantFiles)
			}
			if tt.wantOld != "" || tt.wantNew != "" {
				if files[0].OldPath != tt.wantOld {
					t.Errorf("OldPath = %q, want %q", files[0].OldPath, tt.wantOld)
				}
				if files[0].NewPath != tt.wantNew {
					t.Errorf("NewPath = %q, want %q", files[0].NewPath, tt.wantNew)
				}
			}
			if tt.wantHunks > 0 && len(files[0].Hunks) != tt.wantHunks {
				t.Errorf("got %d hunks, want %d", len(files[0].Hunks), tt.wantHunks)
			}
		})
	}
}

const fiveLines = "one\ntwo\nthree\nfour\nfive\n"

func TestApply(t *testing.T) {
	tests := []struct {
		name    string
		current map[string]string
		patch   string

		wantPath    string
		wantContent string
		wantDeleted bool
		wantErr     string
	}{
		{
			name:    "replaces a line at the stated position",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`,
			wantPath: "SKILL.md", wantContent: "one\nTWO\nthree\nfour\nfive\n",
		},
		{
			name:    "finds the hunk when the file has drifted down",
			current: map[string]string{"SKILL.md": "extra\nlines\nhere\n" + fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`,
			wantPath: "SKILL.md", wantContent: "extra\nlines\nhere\none\nTWO\nthree\nfour\nfive\n",
		},
		{
			name:    "finds the hunk when the file has drifted up",
			current: map[string]string{"SKILL.md": "three\nfour\nfive\n"},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -3,3 +3,3 @@
 three
-four
+FOUR
 five
`,
			wantPath: "SKILL.md", wantContent: "three\nFOUR\nfive\n",
		},
		{
			name:    "two hunks with drift between them",
			current: map[string]string{"SKILL.md": "zero\n" + fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,2 +1,2 @@
-one
+ONE
 two
@@ -4,2 +4,2 @@
-four
+FOUR
 five
`,
			wantPath: "SKILL.md", wantContent: "zero\nONE\ntwo\nthree\nFOUR\nfive\n",
		},
		{
			name:    "insert at the beginning",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,1 +1,2 @@
+zero
 one
`,
			wantPath: "SKILL.md", wantContent: "zero\n" + fiveLines,
		},
		{
			name:    "append at the end",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -5,1 +5,2 @@
 five
+six
`,
			wantPath: "SKILL.md", wantContent: fiveLines + "six\n",
		},
		{
			name:    "delete a line",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -2,3 +2,2 @@
 two
-three
 four
`,
			wantPath: "SKILL.md", wantContent: "one\ntwo\nfour\nfive\n",
		},
		{
			name:    "replace the whole file",
			current: map[string]string{"SKILL.md": "one\ntwo\n"},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,2 +1,1 @@
-one
-two
+only
`,
			wantPath: "SKILL.md", wantContent: "only\n",
		},
		{
			name:    "create a file",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- /dev/null
+++ references/new.md
@@ -0,0 +1,2 @@
+alpha
+beta
`,
			wantPath: "references/new.md", wantContent: "alpha\nbeta\n",
		},
		{
			name:    "delete a file",
			current: map[string]string{"references/old.txt": "a\nb\n"},
			patch: `--- references/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-a
-b
`,
			wantPath: "references/old.txt", wantDeleted: true,
		},
		{
			name:    "result keeps a missing final newline",
			current: map[string]string{"SKILL.md": "one\ntwo"},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,2 +1,2 @@
 one
-two
\ No newline at end of file
+TWO
\ No newline at end of file
`,
			wantPath: "SKILL.md", wantContent: "one\nTWO",
		},
		{
			name:    "result gains a final newline",
			current: map[string]string{"SKILL.md": "one\ntwo"},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,2 +1,2 @@
 one
-two
\ No newline at end of file
+two
`,
			wantPath: "SKILL.md", wantContent: "one\ntwo\n",
		},
		{
			name:    "an untouched missing final newline survives",
			current: map[string]string{"SKILL.md": "one\ntwo\nthree"},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,1 +1,1 @@
-one
+ONE
`,
			wantPath: "SKILL.md", wantContent: "ONE\ntwo\nthree",
		},
		{
			name:    "context mismatch",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
-TWO
+two
 three
`,
			wantErr: "does not apply to SKILL.md",
		},
		{
			name:    "crlf base against an lf patch",
			current: map[string]string{"SKILL.md": "one\r\ntwo\r\nthree\r\n"},
			patch: `--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`,
			wantErr: "does not apply to SKILL.md",
		},
		{
			name:    "unknown file",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- references/missing.md
+++ references/missing.md
@@ -1 +1 @@
-a
+b
`,
			wantErr: "does not exist",
		},
		{
			name:    "create over an existing file",
			current: map[string]string{"SKILL.md": fiveLines},
			patch: `--- /dev/null
+++ SKILL.md
@@ -0,0 +1 @@
+alpha
`,
			wantErr: "already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := Parse(tt.patch)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			changes, err := Apply(files, tt.current)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Apply succeeded, want error containing %q", tt.wantErr)
				}
				var aerr *ApplyError
				if !errors.As(err, &aerr) {
					t.Errorf("error is %T, want *ApplyError", err)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(changes) != 1 {
				t.Fatalf("got %d changes, want 1", len(changes))
			}
			got := changes[0]
			if got.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", got.Path, tt.wantPath)
			}
			if got.Deleted != tt.wantDeleted {
				t.Errorf("Deleted = %v, want %v", got.Deleted, tt.wantDeleted)
			}
			if got.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", got.Content, tt.wantContent)
			}
		})
	}
}

// TestApplyReportsTheFailingHunk pins the detail an agent needs to fix its own
// patch: which hunk, where it looked, and what was actually there.
func TestApplyReportsTheFailingHunk(t *testing.T) {
	files, err := Parse(`--- SKILL.md
+++ SKILL.md
@@ -1,1 +1,1 @@
-one
+ONE
@@ -4,3 +4,3 @@
 four
-FIVE
+cinq
 six
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = Apply(files, map[string]string{"SKILL.md": "one\ntwo\nthree\nfour\nfive\nsix\n"})

	var aerr *ApplyError
	if !errors.As(err, &aerr) {
		t.Fatalf("error is %T, want *ApplyError: %v", err, err)
	}
	if aerr.HunkIndex != 2 {
		t.Errorf("HunkIndex = %d, want 2", aerr.HunkIndex)
	}
	if aerr.Path != "SKILL.md" {
		t.Errorf("Path = %q, want SKILL.md", aerr.Path)
	}
	if got, want := aerr.Expected, []string{"four", "FIVE", "six"}; !equal(got, want) {
		t.Errorf("Expected = %q, want %q", got, want)
	}
	if got, want := aerr.Found, []string{"four", "five", "six"}; !equal(got, want) {
		t.Errorf("Found = %q, want %q", got, want)
	}
	for _, want := range []string{"hunk #2", "@@ -4,3 +4,3 @@", "| four", "| FIVE", "| five"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q is missing %q", err, want)
		}
	}
}

// TestApplyIsAllOrNothing keeps a multi-file patch from landing halfway: the
// first file applies cleanly, the second does not, and neither is returned.
func TestApplyIsAllOrNothing(t *testing.T) {
	files, err := Parse(`--- SKILL.md
+++ SKILL.md
@@ -1 +1 @@
-one
+ONE
--- references/old.txt
+++ references/old.txt
@@ -1 +1 @@
-nope
+b
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	changes, err := Apply(files, map[string]string{
		"SKILL.md":           "one\n",
		"references/old.txt": "a\n",
	})
	if err == nil {
		t.Fatal("Apply succeeded, want an error")
	}
	if changes != nil {
		t.Errorf("got %d changes, want none", len(changes))
	}
}

func TestAlreadyApplied(t *testing.T) {
	files, err := Parse(`--- SKILL.md
+++ SKILL.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !AlreadyApplied(files, map[string]string{"SKILL.md": "one\nTWO\nthree\n"}) {
		t.Error("AlreadyApplied = false for content that already has the change")
	}
	if AlreadyApplied(files, map[string]string{"SKILL.md": "one\nsomething else\nthree\n"}) {
		t.Error("AlreadyApplied = true for unrelated content")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

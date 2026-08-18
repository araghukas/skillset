package skill

import (
	"reflect"
	"testing"
)

// metadataFieldCount is the number of fields on Metadata. Clone must copy
// every one of them, and every reference-typed one must be deep-copied.
// If this fails, a field was added: extend Clone (and the aliasing test
// below) rather than just bumping the number.
const metadataFieldCount = 9

func TestMetadataFieldCountMatchesClone(t *testing.T) {
	if got := reflect.TypeOf(Metadata{}).NumField(); got != metadataFieldCount {
		t.Fatalf("Metadata has %d fields, expected %d; if you added a field, "+
			"make sure Clone copies it (deep-copying it if it is a map, slice, or pointer) "+
			"and that TestCloneSharesNothingMutable covers it", got, metadataFieldCount)
	}
}

func fullMetadata() *Metadata {
	return &Metadata{
		Name:          "example",
		Description:   "does a thing",
		License:       "MIT",
		Compatibility: "requires docker",
		Metadata:      map[string]string{"category": "ops", "json_schema": "{}"},
		AllowedTools:  "bash read",
		JSONSchema:    "{}",
		ContextFiles: []ContextFile{
			{FilePath: "SKILL.md", Content: "# Example", MimeType: "text/markdown"},
			{FilePath: "scripts/run.sh", Content: "#!/bin/sh\n", MimeType: "text/x-shellscript"},
		},
		Commit: "abc123",
	}
}

func TestCloneCopiesEveryField(t *testing.T) {
	orig := fullMetadata()
	got := orig.Clone()

	if !reflect.DeepEqual(orig, got) {
		t.Fatalf("Clone is not equal to the original:\n got %+v\nwant %+v", got, orig)
	}
}

// TestCloneSharesNothingMutable is the real point of Clone. The registry's
// copy of a skill is read concurrently by every request on a replica; if a
// clone shares its map or its context-file backing array, one request
// mutating its copy corrupts the index for all the others. That surfaces as
// skills intermittently losing content under load - a bug that reads like a
// cache problem and is very hard to trace back to here.
func TestCloneSharesNothingMutable(t *testing.T) {
	orig := fullMetadata()
	clone := orig.Clone()

	clone.Metadata["category"] = "mutated"
	clone.Metadata["added"] = "new"
	clone.ContextFiles[0].Content = "mutated"
	clone.ContextFiles = append(clone.ContextFiles, ContextFile{FilePath: "extra"})
	clone.Name = "mutated"

	if got := orig.Metadata["category"]; got != "ops" {
		t.Errorf("mutating the clone's Metadata map changed the original: category = %q, want %q", got, "ops")
	}
	if _, ok := orig.Metadata["added"]; ok {
		t.Error("adding a key to the clone's Metadata map added it to the original")
	}
	if got := orig.ContextFiles[0].Content; got != "# Example" {
		t.Errorf("mutating the clone's ContextFiles changed the original: content = %q, want %q", got, "# Example")
	}
	if got := len(orig.ContextFiles); got != 2 {
		t.Errorf("appending to the clone's ContextFiles changed the original: len = %d, want 2", got)
	}
	if orig.Name != "example" {
		t.Errorf("mutating the clone's Name changed the original: %q", orig.Name)
	}
}

func TestWithoutContextFilesLeavesOriginalIntact(t *testing.T) {
	orig := fullMetadata()
	stripped := orig.WithoutContextFiles()

	if stripped.ContextFiles != nil {
		t.Errorf("WithoutContextFiles kept %d context files", len(stripped.ContextFiles))
	}
	if got := len(orig.ContextFiles); got != 2 {
		t.Fatalf("WithoutContextFiles mutated the original: %d context files left, want 2", got)
	}

	stripped.Metadata["category"] = "mutated"
	if got := orig.Metadata["category"]; got != "ops" {
		t.Errorf("WithoutContextFiles shared the Metadata map: category = %q, want %q", got, "ops")
	}

	// Everything except ContextFiles should survive.
	if stripped.Name != orig.Name || stripped.Commit != orig.Commit || stripped.JSONSchema != orig.JSONSchema {
		t.Errorf("WithoutContextFiles dropped scalar fields: %+v", stripped)
	}
}

func TestCloneNilIsNil(t *testing.T) {
	var m *Metadata
	if m.Clone() != nil {
		t.Error("(*Metadata)(nil).Clone() did not return nil")
	}
	if m.WithoutContextFiles() != nil {
		t.Error("(*Metadata)(nil).WithoutContextFiles() did not return nil")
	}
}

func TestContextFile(t *testing.T) {
	md := fullMetadata()

	cf, ok := md.ContextFile("scripts/run.sh")
	if !ok {
		t.Fatal("ContextFile did not find scripts/run.sh")
	}
	if cf.Content != "#!/bin/sh\n" {
		t.Errorf("ContextFile returned the wrong file: %+v", cf)
	}

	if _, ok := md.ContextFile("nope"); ok {
		t.Error("ContextFile found a file that does not exist")
	}
}

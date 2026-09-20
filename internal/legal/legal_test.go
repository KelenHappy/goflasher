package legal

import (
	"strings"
	"testing"
)

// TestDocumentsCoverEveryCompiledModule is the compliance guard: every entry in
// the generated index must reach the dialog with a non-empty body, because an
// empty entry would present the user with a component whose license text never
// actually shipped.
func TestDocumentsCoverEveryCompiledModule(t *testing.T) {
	documents := Documents()
	if len(documents) <= len(projectDocuments) {
		t.Fatalf("documents=%d, want more than the %d project texts", len(documents), len(projectDocuments))
	}
	for _, document := range documents {
		if document.Title == "" {
			t.Error("document with empty title")
		}
		if strings.TrimSpace(document.Body) == "" {
			t.Errorf("%s has an empty body", document.Title)
		}
	}
}

// TestProjectDocumentsCarryRequiredTexts checks the three texts that GPLv3 and
// the notices policy require to travel with the binary.
func TestProjectDocumentsCarryRequiredTexts(t *testing.T) {
	documents := Documents()
	if !strings.Contains(documents[0].Body, "GNU GENERAL PUBLIC LICENSE") {
		t.Error("first document is not the GPL text")
	}
	for i, want := range []string{"Third-party notices", "第三方"} {
		if !strings.Contains(documents[i+1].Body, want) {
			t.Errorf("document %d does not contain %q", i+1, want)
		}
	}
}

// TestModuleTitlesAreUnique keeps the accordion from showing two entries a user
// cannot tell apart, which would hide one component's terms behind another's.
func TestModuleTitlesAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, document := range Documents() {
		if seen[document.Title] {
			t.Errorf("duplicate title %q", document.Title)
		}
		seen[document.Title] = true
	}
}

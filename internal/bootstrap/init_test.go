package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"forgeworld"
)

func TestEnsurePromptFilesWritesEmbeddedTemplates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	written, err := EnsurePromptFiles(false)
	if err != nil {
		t.Fatalf("EnsurePromptFiles returned error: %v", err)
	}
	if len(written) != 4 {
		t.Fatalf("EnsurePromptFiles wrote %d files, want 4", len(written))
	}

	want, err := forgeworld.TemplateFS.ReadFile("templates/prompts/alpha.md")
	if err != nil {
		t.Fatalf("ReadFile(alpha.md) returned error: %v", err)
	}

	gotPath := filepath.Join(home, ".config", "forgeworld", "alpha.md")
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", gotPath, err)
	}
	if string(got) != string(want) {
		t.Fatalf("alpha.md content mismatch")
	}
}

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forgeworld"
)

func TestEnsurePromptFilesOnlyWritesOnRecreate(t *testing.T) {
	root := t.TempDir()

	// Without --recreate: no files written
	written, err := EnsurePromptFiles(root, false)
	if err != nil {
		t.Fatalf("EnsurePromptFiles returned error: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("EnsurePromptFiles wrote %d files without --recreate, want 0", len(written))
	}

	// With --recreate: all templates written
	written, err = EnsurePromptFiles(root, true)
	if err != nil {
		t.Fatalf("EnsurePromptFiles --recreate returned error: %v", err)
	}
	if len(written) != 9 {
		t.Fatalf("EnsurePromptFiles --recreate wrote %d files, want 9", len(written))
	}

	want, err := forgeworld.TemplateFS.ReadFile("templates/prompts/alpha.md")
	if err != nil {
		t.Fatalf("ReadFile(alpha.md) returned error: %v", err)
	}
	gotPath := filepath.Join(root, "loop", "prompts", "alpha.md")
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", gotPath, err)
	}
	if string(got) != string(want) {
		t.Fatalf("alpha.md content mismatch")
	}
}

func TestEnsureLayoutCreatesPlanTasksDirectory(t *testing.T) {
	root := t.TempDir()

	created, err := EnsureLayout(root, "")
	if err != nil {
		t.Fatalf("EnsureLayout returned error: %v", err)
	}
	if len(created) == 0 {
		t.Fatalf("EnsureLayout should create initial files")
	}

	// Verify plan/tasks/ directory was created
	tasksDir := filepath.Join(root, "plan", "tasks")
	if _, err := os.Stat(tasksDir); err != nil {
		t.Fatalf("expected plan/tasks/ to be created: %v", err)
	}

	// Verify README describes the new format
	readmePath := filepath.Join(root, "plan", "README.md")
	got, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", readmePath, err)
	}

	content := string(got)
	checks := []string{
		"plan/tasks/",
		"plan/plan.md",
		"model: small",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Fatalf("README missing content %q", check)
		}
	}

	// Verify plan/plan.md was created on fresh init
	planMdPath := filepath.Join(root, "plan", "plan.md")
	if _, err := os.Stat(planMdPath); err != nil {
		t.Fatalf("expected plan/plan.md to be created: %v", err)
	}
}

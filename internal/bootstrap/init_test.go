package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
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
	if len(written) != 6 {
		t.Fatalf("EnsurePromptFiles wrote %d files, want 6", len(written))
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

func TestEnsureLayoutWritesPlanReadmeWithParallelMigrationGuidance(t *testing.T) {
	root := t.TempDir()

	created, err := EnsureLayout(root, "")
	if err != nil {
		t.Fatalf("EnsureLayout returned error: %v", err)
	}
	if len(created) == 0 {
		t.Fatalf("EnsureLayout should create initial files")
	}

	readmePath := filepath.Join(root, "plan", "README.md")
	got, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", readmePath, err)
	}

	content := string(got)
	checks := []string{
		"Si encuentras un nodo `parallel`, debes convertirlo a varias tareas simples dentro de la misma fase.",
		"La migracion no conserva ejecucion paralela real; cada tarea resultante se ejecuta como sesion independiente del runtime.",
		"Ejemplo de migracion:",
		"se convierte en:",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Fatalf("README missing guidance %q", check)
		}
	}
}

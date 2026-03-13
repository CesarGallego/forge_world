package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTasksReturnsLegacyErrorWhenOnlyPlanYMLExists(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.yml"), []byte("version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadTasks(root)
	if err == nil {
		t.Fatal("expected error for legacy plan")
	}
	if err != ErrLegacyPlanDetected {
		t.Fatalf("expected ErrLegacyPlanDetected, got %v", err)
	}
}

func TestLoadTasksParsesFrontmatterAndBody(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Crear API\n\nDescripcion de la tarea.\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-crear-api.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tasks, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Filename != "001-crear-api.md" {
		t.Fatalf("expected filename, got %q", task.Filename)
	}
	if task.Name != "Crear API" {
		t.Fatalf("expected name 'Crear API', got %q", task.Name)
	}
	if task.Model != ModelSmall {
		t.Fatalf("expected small model, got %q", task.Model)
	}
	if task.Complete {
		t.Fatal("expected complete to be false")
	}
}

func TestLoadTasksOrdersByFilename(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"003-c.md": "---\nmodel: small\ncomplete: false\n---\n# C\n",
		"001-a.md": "---\nmodel: small\ncomplete: false\n---\n# A\n",
		"002-b.md": "---\nmodel: small\ncomplete: false\n---\n# B\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, "plan", "tasks", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tasks, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Name != "A" || tasks[1].Name != "B" || tasks[2].Name != "C" {
		t.Fatalf("expected ordered tasks A, B, C; got %v, %v, %v", tasks[0].Name, tasks[1].Name, tasks[2].Name)
	}
}

func TestSaveTaskCompleteUpdatesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Mi Tarea\n\nContenido.\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-mi-tarea.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &Task{Filename: "001-mi-tarea.md", Name: "Mi Tarea", Model: ModelSmall, Complete: false}
	if err := SaveTaskComplete(root, task); err != nil {
		t.Fatalf("SaveTaskComplete failed: %v", err)
	}

	updated, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks after save failed: %v", err)
	}
	if len(updated) != 1 || !updated[0].Complete {
		t.Fatal("expected task to be marked complete after SaveTaskComplete")
	}
}

func TestValidateTasksChecksModelAndName(t *testing.T) {
	tasks := []*Task{
		{Filename: "001-a.md", Name: "A", Model: ModelSmall},
		{Filename: "002-b.md", Name: "", Model: ModelSmall},
		{Filename: "003-c.md", Name: "C", Model: "invalid"},
	}

	errs := ValidateTasks(tasks)
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %d: %v", len(errs), errs)
	}
}

func TestNextTaskReturnsFirstIncomplete(t *testing.T) {
	tasks := []*Task{
		{Name: "A", Complete: true},
		{Name: "B", Complete: false},
		{Name: "C", Complete: false},
	}
	next, ok := NextTask(tasks)
	if !ok {
		t.Fatal("expected a next task")
	}
	if next.Name != "B" {
		t.Fatalf("expected 'B', got %q", next.Name)
	}
}

func TestNextTaskReturnsNilWhenAllComplete(t *testing.T) {
	tasks := []*Task{
		{Name: "A", Complete: true},
	}
	_, ok := NextTask(tasks)
	if ok {
		t.Fatal("expected no next task")
	}
}

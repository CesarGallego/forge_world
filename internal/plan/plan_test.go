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
	if task.Slug != "crear-api" {
		t.Fatalf("expected slug 'crear-api', got %q", task.Slug)
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

// TestLoadTasksFallbackAlphabeticalOrderWhenNoPlanMd verifies that without plan/plan.md,
// tasks are ordered alphabetically by filename.
func TestLoadTasksFallbackAlphabeticalOrderWhenNoPlanMd(t *testing.T) {
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

// TestLoadTasksUsesPlanMdOrder verifies that plan/plan.md controls execution order,
// overriding alphabetical filename order.
func TestLoadTasksUsesPlanMdOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"001-a.md": "---\nmodel: small\ncomplete: false\n---\n# A\n",
		"002-b.md": "---\nmodel: small\ncomplete: false\n---\n# B\n",
		"003-c.md": "---\nmodel: small\ncomplete: false\n---\n# C\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, "plan", "tasks", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	planMd := "# Plan\n\n- [ ] c\n- [ ] a\n- [ ] b\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.md"), []byte(planMd), 0o644); err != nil {
		t.Fatal(err)
	}

	tasks, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Name != "C" || tasks[1].Name != "A" || tasks[2].Name != "B" {
		t.Fatalf("expected order C, A, B; got %v, %v, %v", tasks[0].Name, tasks[1].Name, tasks[2].Name)
	}
}

// TestLoadTasksCompletionFromPlanMd verifies plan/plan.md checkboxes override frontmatter.
func TestLoadTasksCompletionFromPlanMd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Crear API\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-crear-api.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	planMd := "# Plan\n\n- [x] crear-api\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.md"), []byte(planMd), 0o644); err != nil {
		t.Fatal(err)
	}

	tasks, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks failed: %v", err)
	}
	if len(tasks) != 1 || !tasks[0].Complete {
		t.Fatal("expected task to be complete (from plan.md checkbox), not from frontmatter")
	}
}

// TestLoadTasksErrorsOnUnknownSlugInPlanMd verifies an error when plan.md references a missing task.
func TestLoadTasksErrorsOnUnknownSlugInPlanMd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Crear API\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-crear-api.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	planMd := "# Plan\n\n- [ ] crear-api\n- [ ] nonexistent\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.md"), []byte(planMd), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadTasks(root)
	if err == nil {
		t.Fatal("expected error for unknown slug in plan.md")
	}
}

// TestLoadTasksErrorsOnOrphanTaskFile verifies an error when a task file is not in plan.md.
func TestLoadTasksErrorsOnOrphanTaskFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"001-crear-api.md": "---\nmodel: small\ncomplete: false\n---\n# Crear API\n",
		"002-orphan.md":    "---\nmodel: small\ncomplete: false\n---\n# Orphan\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "plan", "tasks", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	planMd := "# Plan\n\n- [ ] crear-api\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.md"), []byte(planMd), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadTasks(root)
	if err == nil {
		t.Fatal("expected error for orphan task file not in plan.md")
	}
}

// TestSaveTaskCompleteUpdatesPlanMd verifies SaveTaskComplete writes to plan/plan.md when present.
func TestSaveTaskCompleteUpdatesPlanMd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Mi Tarea\n\nContenido.\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-mi-tarea.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	planMd := "# Plan\n\n- [ ] mi-tarea\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.md"), []byte(planMd), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &Task{Filename: "001-mi-tarea.md", Slug: "mi-tarea", Name: "Mi Tarea", Model: ModelSmall}
	if err := SaveTaskComplete(root, task); err != nil {
		t.Fatalf("SaveTaskComplete failed: %v", err)
	}

	updated, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks after save failed: %v", err)
	}
	if len(updated) != 1 || !updated[0].Complete {
		t.Fatal("expected task to be marked complete in plan.md")
	}
}

// TestSaveTaskCompleteFallbackUpdatesFile verifies frontmatter is updated when plan.md is absent.
func TestSaveTaskCompleteFallbackUpdatesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Mi Tarea\n\nContenido.\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-mi-tarea.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &Task{Filename: "001-mi-tarea.md", Slug: "mi-tarea", Name: "Mi Tarea", Model: ModelSmall}
	if err := SaveTaskComplete(root, task); err != nil {
		t.Fatalf("SaveTaskComplete failed: %v", err)
	}

	updated, err := LoadTasks(root)
	if err != nil {
		t.Fatalf("LoadTasks after save failed: %v", err)
	}
	if len(updated) != 1 || !updated[0].Complete {
		t.Fatal("expected task to be marked complete in frontmatter")
	}
}

func TestValidateTasksChecksModelAndName(t *testing.T) {
	tasks := []*Task{
		{Filename: "001-a.md", Slug: "a", Name: "A", Model: ModelSmall},
		{Filename: "002-b.md", Slug: "b", Name: "", Model: ModelSmall},
		{Filename: "003-c.md", Slug: "c", Name: "C", Model: "invalid"},
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

func TestParsePlanMd(t *testing.T) {
	content := `# Plan

- [ ] crear-api
- [x] añadir-tests
- [X] deploy
some ignored line
`
	entries, err := parsePlanMd(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Slug != "crear-api" || entries[0].Complete {
		t.Fatalf("unexpected entry[0]: %+v", entries[0])
	}
	if entries[1].Slug != "añadir-tests" || !entries[1].Complete {
		t.Fatalf("unexpected entry[1]: %+v", entries[1])
	}
	if entries[2].Slug != "deploy" || !entries[2].Complete {
		t.Fatalf("unexpected entry[2]: %+v", entries[2])
	}
}

func TestSlugFromFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"001-crear-api.md", "crear-api"},
		{"002-añadir-tests.md", "añadir-tests"},
		{"crear-api.md", "crear-api"},
		{"123-foo-bar.md", "foo-bar"},
	}
	for _, c := range cases {
		got := slugFromFilename(c.in)
		if got != c.want {
			t.Errorf("slugFromFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

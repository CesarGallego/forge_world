package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrLegacyPlanDetected is returned when plan/plan.yml exists but plan/tasks/ does not.
var ErrLegacyPlanDetected = errors.New("se detectó plan/plan.yml (formato legacy). El nuevo formato usa plan/tasks/*.md.\nCrea plan/tasks/ y migra tus tareas como ficheros markdown")

type taskFrontmatter struct {
	Model    string `yaml:"model"`
	Complete bool   `yaml:"complete"`
}

type planMdEntry struct {
	Slug     string
	Complete bool
}

// slugFromFilename derives the slug from a task filename.
// "001-crear-api.md" → "crear-api"
// "crear-api.md"     → "crear-api"
func slugFromFilename(filename string) string {
	s := strings.TrimSuffix(filename, ".md")
	if idx := strings.Index(s, "-"); idx >= 0 {
		prefix := s[:idx]
		allDigits := len(prefix) > 0
		for _, c := range prefix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			s = s[idx+1:]
		}
	}
	return s
}

// parsePlanMd parses plan/plan.md and returns an ordered list of entries.
// Lines matching "- [ ] slug" or "- [x] slug" are parsed; all others are ignored.
// Numeric prefixes like "001-" are stripped from slugs automatically.
func parsePlanMd(content string) ([]planMdEntry, error) {
	var entries []planMdEntry
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "- [ ] "):
			slug := slugFromFilename(strings.TrimSpace(line[6:]))
			if slug != "" {
				entries = append(entries, planMdEntry{Slug: slug, Complete: false})
			}
		case strings.HasPrefix(line, "- [x] "), strings.HasPrefix(line, "- [X] "):
			slug := slugFromFilename(strings.TrimSpace(line[6:]))
			if slug != "" {
				entries = append(entries, planMdEntry{Slug: slug, Complete: true})
			}
		}
	}
	return entries, nil
}

// writePlanMdCheckbox rewrites plan/plan.md setting the checkbox for slug to checked.
// All other lines are preserved verbatim.
func writePlanMdCheckbox(root, slug string) error {
	path := filepath.Join(root, "plan", "plan.md")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "- [ ] ") {
			lineText := strings.TrimSpace(line[6:])
			if slugFromFilename(lineText) == slug {
				lines[i] = "- [x] " + lineText
				break
			}
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// LoadTasks loads all task files from plan/tasks/*.md.
// If plan/plan.md exists, it determines execution order and completion status.
// Otherwise falls back to alphabetical order and reads completion from frontmatter.
func LoadTasks(root string) ([]*Task, error) {
	tasksDir := filepath.Join(root, "plan", "tasks")
	legacyPath := filepath.Join(root, "plan", "plan.yml")

	_, tasksDirErr := os.Stat(tasksDir)
	_, legacyErr := os.Stat(legacyPath)

	if os.IsNotExist(tasksDirErr) && legacyErr == nil {
		return nil, ErrLegacyPlanDetected
	}
	if os.IsNotExist(tasksDirErr) {
		return nil, fmt.Errorf("plan/tasks/ no existe; ejecuta `forgeworld init`")
	}

	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, err
	}

	// Load all task files into a map by slug.
	tasksBySlug := map[string]*Task{}
	var allFilenames []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		allFilenames = append(allFilenames, e.Name())
		path := filepath.Join(tasksDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		task, err := parseTaskFile(e.Name(), string(b))
		if err != nil {
			return nil, fmt.Errorf("parsear %s: %w", e.Name(), err)
		}
		tasksBySlug[task.Slug] = task
	}

	// Try plan/plan.md — if present, use it for order and completion.
	planMdPath := filepath.Join(root, "plan", "plan.md")
	b, err := os.ReadFile(planMdPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		planEntries, err := parsePlanMd(string(b))
		if err != nil {
			return nil, err
		}

		// Build ordered list with completion from plan.md.
		// Slugs in plan.md that don't match any task file are silently skipped.
		tasks := make([]*Task, 0, len(planEntries))
		for _, pe := range planEntries {
			t, ok := tasksBySlug[pe.Slug]
			if !ok {
				continue
			}
			t.Complete = pe.Complete
			tasks = append(tasks, t)
		}
		return tasks, nil
	}

	// Fallback: alphabetical order, complete from frontmatter.
	sort.Strings(allFilenames)
	tasks := make([]*Task, 0, len(allFilenames))
	for _, filename := range allFilenames {
		tasks = append(tasks, tasksBySlug[slugFromFilename(filename)])
	}
	return tasks, nil
}

func parseTaskFile(filename, content string) (*Task, error) {
	var fm taskFrontmatter
	var body string

	if strings.HasPrefix(content, "---\n") {
		rest := content[4:]
		end := strings.Index(rest, "\n---\n")
		if end < 0 {
			return nil, fmt.Errorf("frontmatter sin cerrar en %s", filename)
		}
		fmStr := rest[:end]
		if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
			return nil, err
		}
		body = strings.TrimSpace(rest[end+5:])
	} else {
		body = strings.TrimSpace(content)
	}

	name := ""
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# ") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
	}

	return &Task{
		Filename: filename,
		Slug:     slugFromFilename(filename),
		Name:     name,
		Model:    fm.Model,
		Complete: fm.Complete,
		Body:     body,
	}, nil
}

// SaveTaskComplete marks a task as complete.
// If plan/plan.md exists, updates its checkbox. Otherwise rewrites the task frontmatter.
func SaveTaskComplete(root string, task *Task) error {
	planMdPath := filepath.Join(root, "plan", "plan.md")
	if _, err := os.Stat(planMdPath); err == nil {
		return writePlanMdCheckbox(root, task.Slug)
	}
	return saveTaskCompleteFrontmatter(root, task)
}

func saveTaskCompleteFrontmatter(root string, task *Task) error {
	path := filepath.Join(root, "plan", "tasks", task.Filename)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(b)
	if !strings.HasPrefix(content, "---\n") {
		return fmt.Errorf("%s: no tiene frontmatter", task.Filename)
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return fmt.Errorf("%s: frontmatter sin cerrar", task.Filename)
	}
	fmStr := rest[:end]
	bodyPart := rest[end+5:]

	fmLines := strings.Split(fmStr, "\n")
	for i, line := range fmLines {
		if strings.HasPrefix(strings.TrimSpace(line), "complete:") {
			fmLines[i] = "complete: true"
			break
		}
	}
	newContent := "---\n" + strings.Join(fmLines, "\n") + "\n---\n" + bodyPart
	return os.WriteFile(path, []byte(newContent), 0o644)
}

// ReadGlobalContext reads plan/context.md if it exists, returns empty string otherwise.
func ReadGlobalContext(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "plan", "context.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// NextTask returns the first incomplete task, or nil if all are complete.
func NextTask(tasks []*Task) (*Task, bool) {
	for _, t := range tasks {
		if !t.Complete {
			return t, true
		}
	}
	return nil, false
}

// ValidateTasks validates model and name for all tasks.
func ValidateTasks(tasks []*Task) []error {
	var errs []error
	for _, t := range tasks {
		if strings.TrimSpace(t.Name) == "" {
			errs = append(errs, fmt.Errorf("%s: nombre vacio (falta H1 en el fichero)", t.Filename))
		}
		if err := ValidateModel(t.Model); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Filename, err))
		}
	}
	return errs
}

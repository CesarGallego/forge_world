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

// LoadTasks loads all task files from plan/tasks/*.md, ordered by filename.
// Returns ErrLegacyPlanDetected if plan/plan.yml exists but plan/tasks/ does not.
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

	var filenames []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			filenames = append(filenames, e.Name())
		}
	}
	sort.Strings(filenames)

	tasks := make([]*Task, 0, len(filenames))
	for _, filename := range filenames {
		path := filepath.Join(tasksDir, filename)
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		task, err := parseTaskFile(filename, string(b))
		if err != nil {
			return nil, fmt.Errorf("parsear %s: %w", filename, err)
		}
		tasks = append(tasks, task)
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
		Name:     name,
		Model:    fm.Model,
		Complete: fm.Complete,
		Body:     body,
	}, nil
}

// SaveTaskComplete rewrites the frontmatter of the task file to set complete: true.
func SaveTaskComplete(root string, task *Task) error {
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

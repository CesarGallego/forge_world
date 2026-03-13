package plan

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ModelSmall  = "small"
	ModelMedium = "medium"
	ModelLarge  = "large"
)

// Task represents a single task file in plan/tasks/*.md
type Task struct {
	Filename string // e.g. "001-crear-api.md"
	Name     string // from H1 heading in the file body
	Model    string `yaml:"model"`
	Complete bool   `yaml:"complete"`
	Body     string // markdown content after frontmatter
}

func ValidateModel(model string) error {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case ModelSmall, ModelMedium, ModelLarge:
		return nil
	default:
		return fmt.Errorf("modelo invalido %q: use small|medium|large", model)
	}
}

func EscalateModel(model string) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case ModelSmall:
		return ModelMedium, true, nil
	case ModelMedium:
		return ModelLarge, true, nil
	case ModelLarge:
		return ModelLarge, false, nil
	default:
		return "", false, errors.New("modelo desconocido")
	}
}

func TaskSlug(name string) string {
	s := strings.TrimSpace(strings.ToLower(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

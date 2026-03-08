package plan

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	ModelSmall  = "small"
	ModelMedium = "medium"
	ModelLarge  = "large"

	PhaseTypeUser       = "user"
	PhaseTypeValidation = "validation"
)

type RuntimeState struct {
	Attempts       int    `yaml:"attempts,omitempty"`
	EffectiveModel string `yaml:"effective_model,omitempty"`
	LastReturnCode int    `yaml:"last_returncode,omitempty"`
	LastError      string `yaml:"last_error,omitempty"`
	FeedbackRef    string `yaml:"feedback_ref,omitempty"`
	ResultRef      string `yaml:"result_ref,omitempty"`
}

type Task struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Complete    bool         `yaml:"complete"`
	Model       string       `yaml:"model"`
	Context     string       `yaml:"context,omitempty"`
	State       RuntimeState `yaml:"state,omitempty"`
}

type TaskNode struct {
	Task               *Task                  `yaml:"-"`
	DeprecatedParallel bool                   `yaml:"-"`
	raw                map[string]interface{} `yaml:"-"`
}

type Phase struct {
	Type        string     `yaml:"type,omitempty"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Complete    bool       `yaml:"complete"`
	Context     string     `yaml:"context,omitempty"`
	Tasks       []TaskNode `yaml:"tasks"`
}

type Plan struct {
	Context string  `yaml:"context,omitempty"`
	Phases  []Phase `yaml:"phases"`
}

func (n *TaskNode) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw map[string]interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	if _, ok := raw["parallel"]; ok {
		n.DeprecatedParallel = true
		n.raw = raw
		return nil
	}
	type taskAlias Task
	var t taskAlias
	if err := unmarshal(&t); err != nil {
		return err
	}
	task := Task(t)
	n.Task = &task
	return nil
}

func (n TaskNode) MarshalYAML() (interface{}, error) {
	if n.Task != nil {
		// Single-task nodes are serialized as a plain task object, not wrapped.
		return n.Task, nil
	}
	if n.DeprecatedParallel && n.raw != nil {
		return n.raw, nil
	}
	return map[string]interface{}{}, nil
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

func NormalizePhaseType(phaseType string) string {
	kind := strings.ToLower(strings.TrimSpace(phaseType))
	if kind == "" {
		return PhaseTypeUser
	}
	return kind
}

func ValidatePhaseType(phaseType string) error {
	switch NormalizePhaseType(phaseType) {
	case PhaseTypeUser, PhaseTypeValidation:
		return nil
	default:
		return fmt.Errorf("tipo de fase invalido %q: use user|validation", phaseType)
	}
}

func TaskSlug(name string) string {
	s := strings.TrimSpace(strings.ToLower(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

func ExpectedTaskContextPath(taskName string) string {
	return filepath.ToSlash(filepath.Join("loop", "tasks", TaskSlug(taskName), "context.md"))
}

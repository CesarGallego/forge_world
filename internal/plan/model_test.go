package plan

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTaskNodeRoundTripPreservesSingleTasks(t *testing.T) {
	original := Plan{
		Context: "ctx",
		Phases: []Phase{
			{
				Name:        "Fase 1",
				Description: "descripcion",
				Complete:    false,
				Tasks: []TaskNode{
					{
						Task: &Task{
							Name:        "Tarea simple",
							Description: "desc",
							Complete:    false,
							Model:       ModelSmall,
							Context:     "task-ctx",
						},
					},
				},
			},
		},
	}

	b, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got Plan
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(got.Phases) != 1 {
		t.Fatalf("expected 1 phase, got %d", len(got.Phases))
	}
	if len(got.Phases[0].Tasks) != 1 {
		t.Fatalf("expected 1 task node, got %d", len(got.Phases[0].Tasks))
	}

	single := got.Phases[0].Tasks[0]
	if single.Task == nil {
		t.Fatalf("expected single task node to remain a task")
	}
	if single.Task.Name != "Tarea simple" {
		t.Fatalf("expected single task name to be preserved, got %q", single.Task.Name)
	}
}

func TestTaskNodeUnmarshalDeprecatedParallel(t *testing.T) {
	raw := `
phases:
  - name: Fase
    description: desc
    complete: false
    tasks:
      - parallel:
          - name: P1
            description: d1
            complete: false
            model: small
            context: loop/tasks/p1/context.md
          - name: P2
            description: d2
            complete: false
            model: medium
            context: loop/tasks/p2/context.md
`
	var p Plan
	if err := yaml.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	errs := Validate(&p)
	if len(errs) == 0 {
		t.Fatalf("expected validation error for deprecated parallel")
	}
	if !strings.Contains(errs[0].Error(), "deprecado") {
		t.Fatalf("expected deprecation error, got %v", errs)
	}
}

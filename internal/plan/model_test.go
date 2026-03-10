package plan

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTaskNodeRoundTripPreservesSingleTasks(t *testing.T) {
	original := Plan{
		Version: "2",
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

func TestNormalizeDeprecatedParallelExpandsTasks(t *testing.T) {
	raw := `
version: 2
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
	changed := normalizeDeprecatedParallel(&p)
	if !changed {
		t.Fatalf("expected deprecated parallel nodes to be normalized")
	}
	if len(p.Phases) != 1 || len(p.Phases[0].Tasks) != 2 {
		t.Fatalf("expected 2 sequential tasks after normalization, got %+v", p.Phases)
	}
	if p.Phases[0].Tasks[0].Task == nil || p.Phases[0].Tasks[0].Task.Name != "P1" {
		t.Fatalf("expected first normalized task to be P1, got %+v", p.Phases[0].Tasks[0].Task)
	}
	if p.Phases[0].Tasks[1].Task == nil || p.Phases[0].Tasks[1].Task.Name != "P2" {
		t.Fatalf("expected second normalized task to be P2, got %+v", p.Phases[0].Tasks[1].Task)
	}
	if errs := Validate(&p); len(errs) != 0 {
		t.Fatalf("expected normalized plan to validate, got %v", errs)
	}
}

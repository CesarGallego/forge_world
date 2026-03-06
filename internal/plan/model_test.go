package plan

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTaskNodeRoundTripPreservesSingleAndParallelTasks(t *testing.T) {
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
					{
						Parallel: []Task{
							{Name: "P1", Description: "d1", Complete: false, Model: ModelMedium},
							{Name: "P2", Description: "d2", Complete: false, Model: ModelLarge},
						},
						Context: "parallel-ctx",
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
	if len(got.Phases[0].Tasks) != 2 {
		t.Fatalf("expected 2 task nodes, got %d", len(got.Phases[0].Tasks))
	}

	single := got.Phases[0].Tasks[0]
	if single.Task == nil {
		t.Fatalf("expected single task node to remain a task")
	}
	if single.Task.Name != "Tarea simple" {
		t.Fatalf("expected single task name to be preserved, got %q", single.Task.Name)
	}

	parallel := got.Phases[0].Tasks[1]
	if len(parallel.Parallel) != 2 {
		t.Fatalf("expected 2 parallel tasks, got %d", len(parallel.Parallel))
	}
	if parallel.Parallel[0].Name != "P1" || parallel.Parallel[1].Name != "P2" {
		t.Fatalf("parallel tasks were not preserved: %+v", parallel.Parallel)
	}
}

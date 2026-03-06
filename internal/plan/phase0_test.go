package plan

import "testing"

func TestEnsurePhase0AddsMergeConsolidationTask(t *testing.T) {
	p := &Plan{
		Phases: []Phase{
			{
				Type:        PhaseTypeValidation,
				Name:        "Preparacion del bucle de forja",
				Description: "fase",
				Complete:    false,
				Tasks: []TaskNode{
					{Task: &Task{Name: "Validar estructura del plan", Complete: false, Model: ModelSmall}},
					{Task: &Task{Name: "Crear skills base", Complete: false, Model: ModelSmall}},
					{Task: &Task{Name: "Agregar tareas de validacion", Complete: false, Model: ModelMedium}},
				},
			},
		},
	}

	changed := EnsurePhase0(p)
	if !changed {
		t.Fatalf("expected EnsurePhase0 to add missing validation tasks")
	}

	found := false
	for _, node := range p.Phases[0].Tasks {
		if node.Task != nil && node.Task.Name == "Agregar fase de consolidacion de merges" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected merge consolidation task to be present in phase 0")
	}
}

package plan

import (
	"strings"
	"testing"
)

func TestValidateRequiresExpectedTaskContextInUserPhase(t *testing.T) {
	p := &Plan{
		Version: "2",
		Phases: []Phase{
			{
				Type:        PhaseTypeUser,
				Name:        "Implementacion",
				Description: "fase",
				Complete:    false,
				Tasks: []TaskNode{
					{Task: &Task{Name: "Crear API", Description: "desc", Complete: false, Model: ModelSmall, Context: "loop/tasks/crear-api/context.md"}},
				},
			},
		},
	}

	errs := Validate(p)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRejectsWrongTaskContextPathInUserPhase(t *testing.T) {
	p := &Plan{
		Version: "2",
		Phases: []Phase{
			{
				Type:        PhaseTypeUser,
				Name:        "Implementacion",
				Description: "fase",
				Complete:    false,
				Tasks: []TaskNode{
					{Task: &Task{Name: "Crear API", Description: "desc", Complete: false, Model: ModelSmall, Context: "docs/contexto.md"}},
				},
			},
		},
	}

	errs := Validate(p)
	if len(errs) == 0 {
		t.Fatalf("expected validation errors")
	}
	if !strings.Contains(errs[0].Error(), "context invalido") {
		t.Fatalf("expected context error, got %v", errs)
	}
}

func TestValidateDoesNotRequireTaskContextInValidationPhase(t *testing.T) {
	p := &Plan{
		Version: "2",
		Phases: []Phase{
			{
				Type:        PhaseTypeValidation,
				Name:        "Fase interna",
				Description: "fase",
				Complete:    false,
				Tasks: []TaskNode{
					{Task: &Task{Name: "Validar", Description: "desc", Complete: false, Model: ModelSmall}},
				},
			},
		},
	}

	errs := Validate(p)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

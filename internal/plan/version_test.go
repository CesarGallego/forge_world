package plan

import "testing"

func TestValidateRequiresVersion(t *testing.T) {
	p := &Plan{
		Phases: []Phase{
			{
				Name:        "Fase",
				Description: "desc",
				Tasks:       []TaskNode{{Task: &Task{Name: "T1", Description: "d", Model: ModelSmall, Context: "loop/tasks/t1/context.md"}}},
			},
		},
	}

	errs := Validate(p)
	if len(errs) == 0 {
		t.Fatalf("expected version validation error")
	}
}

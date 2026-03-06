package engine

import (
	"testing"

	"forgeworld/internal/plan"
)

func TestRestoreCompletedTasksKeepsDoneOnReloadedPlan(t *testing.T) {
	p := &plan.Plan{
		Phases: []plan.Phase{
			{
				Name: "F1",
				Tasks: []plan.TaskNode{
					{Task: &plan.Task{Name: "T1", Complete: true, Model: plan.ModelSmall}},
					{Task: &plan.Task{Name: "T2", Complete: false, Model: plan.ModelSmall}},
				},
			},
		},
	}

	done := snapshotCompletedTasks(p)
	reloaded := &plan.Plan{
		Phases: []plan.Phase{
			{
				Name: "F1",
				Tasks: []plan.TaskNode{
					{Task: &plan.Task{Name: "T1", Complete: false, Model: plan.ModelSmall}},
					{Task: &plan.Task{Name: "T2", Complete: false, Model: plan.ModelSmall}},
				},
			},
		},
	}

	restoreCompletedTasks(reloaded, done)

	if !reloaded.Phases[0].Tasks[0].Task.Complete {
		t.Fatalf("T1 deberia permanecer completada tras recarga")
	}
	if reloaded.Phases[0].Tasks[1].Task.Complete {
		t.Fatalf("T2 no deberia marcarse completada")
	}
}

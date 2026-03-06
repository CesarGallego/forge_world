package plan

import "testing"

func TestReconcileCompletionUnmarksPhaseWithPendingTasks(t *testing.T) {
	p := &Plan{
		Phases: []Phase{
			{
				Name:     "F1",
				Complete: true,
				Tasks: []TaskNode{
					{Task: &Task{Name: "T1", Complete: true, Model: ModelSmall}},
					{Task: &Task{Name: "T2", Complete: false, Model: ModelSmall}},
				},
			},
		},
	}

	changed := ReconcileCompletion(p)
	if !changed {
		t.Fatalf("expected reconcile to change phase completion")
	}
	if p.Phases[0].Complete {
		t.Fatalf("phase should be unmarked complete when pending tasks exist")
	}
}

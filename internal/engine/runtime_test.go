package engine

import (
	"testing"

	"forgeworld"
	"forgeworld/internal/plan"
)

func TestSyncRuntimeWithPlanFlagsUpgradeMismatch(t *testing.T) {
	rt := &RuntimeState{}
	p := &plan.Plan{
		Version: "1",
		Phases: []plan.Phase{
			{Name: "Fase 1", Description: "desc", Tasks: []plan.TaskNode{{Task: &plan.Task{Name: "T1", Description: "d", Model: plan.ModelSmall}}}},
		},
	}

	syncRuntimeWithPlan(rt, t.TempDir(), p)

	if !rt.UpgradeNeeded {
		t.Fatalf("expected upgrade_needed for legacy plan")
	}
	if rt.Version != forgeworld.CurrentPlanVersion {
		t.Fatalf("unexpected runtime version %q", rt.Version)
	}
}

func TestSyncRuntimeWithPlanMarksPlanCompleteTasksAsMerged(t *testing.T) {
	root := t.TempDir()
	rt := &RuntimeState{
		Phases: []*PhaseRuntime{
			{
				ID:        "phase-01",
				PlanIndex: 0,
				Name:      "Fase",
				Status:    phaseStatusRunning,
				Sessions: []*SessionRuntime{
					{ID: "s01", TaskName: "A", Goal: "A", Model: plan.ModelSmall, Status: sessionStatusRejected, Attempts: 5},
					{ID: "s02", TaskName: "B", Goal: "B", Model: plan.ModelMedium, Status: sessionStatusPlanned},
				},
			},
		},
	}
	p := &plan.Plan{
		Version: "2",
		Phases: []plan.Phase{
			{
				Name: "Fase",
				Tasks: []plan.TaskNode{
					{Task: &plan.Task{Name: "A", Description: "a", Model: plan.ModelSmall, Complete: true}},
					{Task: &plan.Task{Name: "B", Description: "b", Model: plan.ModelMedium, Complete: false}},
				},
			},
		},
	}

	syncRuntimeWithPlan(rt, root, p)

	sessA := rt.Phases[0].Sessions[0]
	if sessA.Status != sessionStatusMerged {
		t.Fatalf("expected session A (complete in plan) to be merged, got %q", sessA.Status)
	}
	if sessA.ReviewVerdict != "approved" {
		t.Fatalf("expected approved verdict for A, got %q", sessA.ReviewVerdict)
	}
	sessB := rt.Phases[0].Sessions[1]
	if sessB.Status != sessionStatusPlanned {
		t.Fatalf("expected session B (incomplete in plan) to remain planned, got %q", sessB.Status)
	}
}

func TestSeedSessionsForPhaseCreatesOneSessionPerPendingTask(t *testing.T) {
	phase := &PhaseRuntime{ID: "phase-01", Name: "Fase"}
	src := plan.Phase{
		Name: "Fase",
		Tasks: []plan.TaskNode{
			{Task: &plan.Task{Name: "A", Description: "a", Model: plan.ModelSmall}},
			{Task: &plan.Task{Name: "B", Description: "b", Model: plan.ModelMedium, Complete: true}},
			{Task: &plan.Task{Name: "C", Description: "c", Model: plan.ModelLarge}},
		},
	}

	seedSessionsForPhase(t.TempDir(), phase, src)

	if len(phase.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(phase.Sessions))
	}
	if phase.Sessions[0].Goal != "A" || phase.Sessions[1].Goal != "C" {
		t.Fatalf("unexpected session goals: %+v", phase.Sessions)
	}
}

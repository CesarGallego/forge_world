package engine

import (
	"testing"

	"forgeworld/internal/plan"
)

func TestSyncRuntimeWithTasksCreatesSessionsForNewTasks(t *testing.T) {
	rt := &RuntimeState{}
	tasks := []*plan.Task{
		{Filename: "001-a.md", Name: "A", Model: plan.ModelSmall},
		{Filename: "002-b.md", Name: "B", Model: plan.ModelMedium},
	}

	syncRuntimeWithTasks(rt, t.TempDir(), tasks)

	if len(rt.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(rt.Sessions))
	}
	if rt.Sessions[0].ID != "s001-a" {
		t.Fatalf("expected session ID s001-a, got %q", rt.Sessions[0].ID)
	}
	if rt.Sessions[1].ID != "s002-b" {
		t.Fatalf("expected session ID s002-b, got %q", rt.Sessions[1].ID)
	}
}

func TestSyncRuntimeWithTasksMarksMergedWhenTaskComplete(t *testing.T) {
	rt := &RuntimeState{
		Sessions: []*SessionRuntime{
			{ID: "s001-a", TaskName: "A", Goal: "A", Model: plan.ModelSmall, Status: sessionStatusRejected, Attempts: 2},
		},
	}
	tasks := []*plan.Task{
		{Filename: "001-a.md", Name: "A", Model: plan.ModelSmall, Complete: true},
	}

	syncRuntimeWithTasks(rt, t.TempDir(), tasks)

	if rt.Sessions[0].Status != sessionStatusMerged {
		t.Fatalf("expected merged, got %q", rt.Sessions[0].Status)
	}
	if rt.Sessions[0].ReviewVerdict != "approved" {
		t.Fatalf("expected approved verdict, got %q", rt.Sessions[0].ReviewVerdict)
	}
}

func TestNextRunnableSessionFlatReturnsFirstPending(t *testing.T) {
	rt := &RuntimeState{
		Sessions: []*SessionRuntime{
			{ID: "s001", Status: sessionStatusMerged},
			{ID: "s002", Status: sessionStatusFailed},
			{ID: "s003", Status: sessionStatusPlanned},
		},
	}
	sess := nextRunnableSessionFlat(rt)
	if sess == nil {
		t.Fatal("expected a runnable session")
	}
	if sess.ID != "s002" {
		t.Fatalf("expected s002, got %q", sess.ID)
	}
}

func TestNextRunnableSessionFlatReturnsNilWhenAllMerged(t *testing.T) {
	rt := &RuntimeState{
		Sessions: []*SessionRuntime{
			{ID: "s001", Status: sessionStatusMerged},
			{ID: "s002", Status: sessionStatusMerged},
		},
	}

	sess := nextRunnableSessionFlat(rt)
	if sess != nil {
		t.Fatalf("expected nil, got %q", sess.ID)
	}
}

func TestSyncRuntimeWithTasksPreservesExistingSession(t *testing.T) {
	root := t.TempDir()
	rt := &RuntimeState{
		Sessions: []*SessionRuntime{
			{ID: "s001-a", TaskName: "A", Goal: "A", Model: plan.ModelSmall, Status: sessionStatusRejected, Attempts: 5},
		},
	}
	tasks := []*plan.Task{
		{Filename: "001-a.md", Name: "A", Model: plan.ModelSmall, Complete: false},
	}

	syncRuntimeWithTasks(rt, root, tasks)

	if len(rt.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(rt.Sessions))
	}
	if rt.Sessions[0].Attempts != 5 {
		t.Fatalf("expected preserved attempts=5, got %d", rt.Sessions[0].Attempts)
	}
	if rt.Sessions[0].Status != sessionStatusRejected {
		t.Fatalf("expected preserved rejected status, got %q", rt.Sessions[0].Status)
	}
}

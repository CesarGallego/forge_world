package plan

import "testing"

func TestMarkDoneMarksPhaseCompleteWhenLastTaskFinishes(t *testing.T) {
	p := &Plan{
		Phases: []Phase{
			{
				Name: "F1",
				Tasks: []TaskNode{
					{Task: &Task{Name: "T1", Model: ModelSmall, Complete: true}},
					{Task: &Task{Name: "T2", Model: ModelSmall, Complete: false}},
				},
			},
		},
	}

	MarkDone(p, TaskRef{PhaseIdx: 0, NodeIdx: 1})

	if !p.Phases[0].Complete {
		t.Fatalf("la fase deberia quedar marcada como completa")
	}
}

func TestMarkDoneDoesNotMarkPhaseCompleteIfInvalidNodeExists(t *testing.T) {
	p := &Plan{
		Phases: []Phase{
			{
				Name: "F1",
				Tasks: []TaskNode{
					{Task: &Task{Name: "T1", Model: ModelSmall, Complete: false}},
					{},
				},
			},
		},
	}

	MarkDone(p, TaskRef{PhaseIdx: 0, NodeIdx: 0})

	if p.Phases[0].Complete {
		t.Fatalf("la fase no deberia marcarse completa con nodos invalidos pendientes")
	}
}

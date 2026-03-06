package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var ErrPlanNotFound = errors.New("plan/plan.yml no existe")

type TaskRef struct {
	PhaseIdx int
	NodeIdx  int
	TaskIdx  int
	IsPair   bool
}

func Load(root string) (*Plan, string, error) {
	path := filepath.Join(root, "plan", "plan.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, ErrPlanNotFound
		}
		return nil, path, err
	}
	var p Plan
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, path, err
	}
	return &p, path, nil
}

func Save(path string, p *Plan) error {
	b, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func Validate(p *Plan) []error {
	var errs []error
	if len(p.Phases) == 0 {
		errs = append(errs, errors.New("el plan debe tener al menos una fase"))
		return errs
	}
	for pi := range p.Phases {
		phase := &p.Phases[pi]
		requireTaskContext := NormalizePhaseType(phase.Type) != PhaseTypeValidation
		if err := ValidatePhaseType(phase.Type); err != nil {
			errs = append(errs, fmt.Errorf("phase[%d].type: %w", pi, err))
		}
		if strings.TrimSpace(phase.Name) == "" {
			errs = append(errs, fmt.Errorf("phase[%d].name no puede estar vacio", pi))
		}
		for ni := range phase.Tasks {
			node := &phase.Tasks[ni]
			if node.Task != nil {
				err := validateTask(node.Task, fmt.Sprintf("phase[%d].tasks[%d]", pi, ni), requireTaskContext)
				if err != nil {
					errs = append(errs, err)
				}
				continue
			}
			if len(node.Parallel) != 2 {
				errs = append(errs, fmt.Errorf("phase[%d].tasks[%d].parallel debe contener exactamente 2 tareas", pi, ni))
				continue
			}
			for ti := range node.Parallel {
				if err := validateTask(&node.Parallel[ti], fmt.Sprintf("phase[%d].tasks[%d].parallel[%d]", pi, ni, ti), requireTaskContext); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errs
}

func EnsurePhase0(p *Plan) bool {
	if len(p.Phases) == 0 {
		p.Phases = []Phase{newPhase0()}
		return true
	}
	if isValidationPhase(&p.Phases[0]) {
		return ensurePhase0Tasks(&p.Phases[0])
	}
	for i := 1; i < len(p.Phases); i++ {
		if !isValidationPhase(&p.Phases[i]) {
			continue
		}
		validation := p.Phases[i]
		p.Phases = append([]Phase{validation}, append(p.Phases[:i], p.Phases[i+1:]...)...)
		_ = ensurePhase0Tasks(&p.Phases[0])
		return true
	}
	p.Phases = append([]Phase{newPhase0()}, p.Phases...)
	return true
}

// ReconcileCompletion desmarca fases marcadas como completas que ya no lo estan
// (por ejemplo, cuando se agregan tareas nuevas a una fase cerrada).
func ReconcileCompletion(p *Plan) bool {
	changed := false
	for pi := range p.Phases {
		phase := &p.Phases[pi]
		if phase.Complete && !isPhaseDone(phase) {
			phase.Complete = false
			changed = true
		}
	}
	return changed
}

func newPhase0() Phase {
	return Phase{
		Type:        PhaseTypeValidation,
		Name:        "Preparacion del bucle de forja",
		Description: "Verifica plan, validaciones y skills base para operar con contexto minimo.",
		Complete:    false,
		Tasks: []TaskNode{
			{Task: &Task{Name: "Validar estructura del plan", Description: "Garantiza que plan.yml cumple estructura y modelos por tarea.", Complete: false, Model: ModelSmall}},
			{Task: &Task{Name: "Crear skills base", Description: "Crea estructura inicial de loop/skills para contexto progresivo.", Complete: false, Model: ModelSmall}},
			{Task: &Task{Name: "Agregar tareas de validacion", Description: "Inserta tareas de comprobacion de resultados faltantes en el plan.", Complete: false, Model: ModelMedium}},
			{Task: &Task{Name: "Agregar fase de consolidacion de merges", Description: "Asegura una fase posterior para consolidar merges de nodos paralelos y verificar commits integrados.", Complete: false, Model: ModelMedium}},
		},
	}
}

func ensurePhase0Tasks(phase *Phase) bool {
	required := []Task{
		{Name: "Validar estructura del plan", Description: "Garantiza que plan.yml cumple estructura y modelos por tarea.", Complete: false, Model: ModelSmall},
		{Name: "Crear skills base", Description: "Crea estructura inicial de loop/skills para contexto progresivo.", Complete: false, Model: ModelSmall},
		{Name: "Agregar tareas de validacion", Description: "Inserta tareas de comprobacion de resultados faltantes en el plan.", Complete: false, Model: ModelMedium},
		{Name: "Agregar fase de consolidacion de merges", Description: "Asegura una fase posterior para consolidar merges de nodos paralelos y verificar commits integrados.", Complete: false, Model: ModelMedium},
	}
	changed := false
	exists := map[string]struct{}{}
	for i := range phase.Tasks {
		node := phase.Tasks[i]
		if node.Task != nil {
			exists[node.Task.Name] = struct{}{}
		}
	}
	for _, task := range required {
		if _, ok := exists[task.Name]; ok {
			continue
		}
		t := task
		phase.Tasks = append(phase.Tasks, TaskNode{Task: &t})
		changed = true
	}
	return changed
}

func isValidationPhase(p *Phase) bool {
	return NormalizePhaseType(p.Type) == PhaseTypeValidation
}

func validateTask(t *Task, path string, requireContext bool) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("%s.name no puede estar vacio", path)
	}
	if err := ValidateModel(t.Model); err != nil {
		return fmt.Errorf("%s.model: %w", path, err)
	}
	if requireContext {
		ctx := strings.TrimSpace(t.Context)
		expected := ExpectedTaskContextPath(t.Name)
		if ctx == "" {
			return fmt.Errorf("%s.context es obligatorio y debe apuntar a %q", path, expected)
		}
		normalized := filepath.ToSlash(filepath.Clean(ctx))
		if strings.HasPrefix(normalized, "./") {
			normalized = strings.TrimPrefix(normalized, "./")
		}
		if normalized != expected {
			return fmt.Errorf("%s.context invalido: esperado %q, recibido %q", path, expected, ctx)
		}
	}
	return nil
}

func NextNode(p *Plan) (TaskRef, TaskRef, bool, bool) {
	for pi := range p.Phases {
		phase := &p.Phases[pi]
		if phase.Complete {
			continue
		}
		allDone := true
		for ni := range phase.Tasks {
			node := &phase.Tasks[ni]
			if node.Task != nil {
				if !node.Task.Complete {
					return TaskRef{PhaseIdx: pi, NodeIdx: ni, TaskIdx: 0, IsPair: false}, TaskRef{}, false, true
				}
				continue
			}
			if !(node.Parallel[0].Complete && node.Parallel[1].Complete) {
				return TaskRef{PhaseIdx: pi, NodeIdx: ni, TaskIdx: 0, IsPair: true}, TaskRef{PhaseIdx: pi, NodeIdx: ni, TaskIdx: 1, IsPair: true}, true, true
			}
		}
		for ni := range phase.Tasks {
			n := &phase.Tasks[ni]
			if n.Task != nil && !n.Task.Complete {
				allDone = false
			}
			if n.Task == nil && !(n.Parallel[0].Complete && n.Parallel[1].Complete) {
				allDone = false
			}
		}
		if allDone {
			phase.Complete = true
		}
	}
	return TaskRef{}, TaskRef{}, false, false
}

func ResolveTask(p *Plan, ref TaskRef) *Task {
	node := &p.Phases[ref.PhaseIdx].Tasks[ref.NodeIdx]
	if node.Task != nil {
		return node.Task
	}
	return &node.Parallel[ref.TaskIdx]
}

func TryResolveTask(p *Plan, ref TaskRef) (*Task, bool) {
	if ref.PhaseIdx < 0 || ref.PhaseIdx >= len(p.Phases) {
		return nil, false
	}
	phase := &p.Phases[ref.PhaseIdx]
	if ref.NodeIdx < 0 || ref.NodeIdx >= len(phase.Tasks) {
		return nil, false
	}
	node := &phase.Tasks[ref.NodeIdx]
	if node.Task != nil {
		if ref.TaskIdx != 0 {
			return nil, false
		}
		return node.Task, true
	}
	if ref.TaskIdx < 0 || ref.TaskIdx >= len(node.Parallel) {
		return nil, false
	}
	return &node.Parallel[ref.TaskIdx], true
}

func FindTaskRefByName(p *Plan, name string) (TaskRef, bool) {
	for pi := range p.Phases {
		for ni := range p.Phases[pi].Tasks {
			node := &p.Phases[pi].Tasks[ni]
			if node.Task != nil {
				if node.Task.Name == name {
					return TaskRef{PhaseIdx: pi, NodeIdx: ni, TaskIdx: 0, IsPair: false}, true
				}
				continue
			}
			for ti := range node.Parallel {
				if node.Parallel[ti].Name == name {
					return TaskRef{PhaseIdx: pi, NodeIdx: ni, TaskIdx: ti, IsPair: true}, true
				}
			}
		}
	}
	return TaskRef{}, false
}

func BuildContext(p *Plan, ref TaskRef) string {
	parts := []string{}
	if strings.TrimSpace(p.Context) != "" {
		parts = append(parts, p.Context)
	}
	phase := p.Phases[ref.PhaseIdx]
	if strings.TrimSpace(phase.Context) != "" {
		parts = append(parts, phase.Context)
	}
	node := phase.Tasks[ref.NodeIdx]
	if strings.TrimSpace(node.Context) != "" {
		parts = append(parts, node.Context)
	}
	t := ResolveTask(p, ref)
	if strings.TrimSpace(t.Context) != "" {
		parts = append(parts, t.Context)
	}
	return strings.Join(parts, "\n\n")
}

func MarkDone(p *Plan, ref TaskRef) {
	t := ResolveTask(p, ref)
	t.Complete = true
	phase := &p.Phases[ref.PhaseIdx]
	if isPhaseDone(phase) {
		phase.Complete = true
	}
}

func isPhaseDone(phase *Phase) bool {
	for ni := range phase.Tasks {
		node := &phase.Tasks[ni]
		if node.Task != nil {
			if !node.Task.Complete {
				return false
			}
			continue
		}
		if len(node.Parallel) != 2 {
			return false
		}
		if !node.Parallel[0].Complete || !node.Parallel[1].Complete {
			return false
		}
	}
	return true
}

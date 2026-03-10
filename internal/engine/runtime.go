package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forgeworld"
	"forgeworld/internal/plan"

	"gopkg.in/yaml.v3"
)

const (
	sessionStatusPlanned       = "planned"
	sessionStatusRunning       = "running"
	sessionStatusReviewPending = "review_pending"
	sessionStatusApproved      = "approved"
	sessionStatusRejected      = "rejected"
	sessionStatusMerged        = "merged"
	sessionStatusFailed        = "failed"

	phaseStatusPending   = "pending"
	phaseStatusRunning   = "running"
	phaseStatusCompleted = "completed"
)

type RuntimeState struct {
	Version       string          `yaml:"version"`
	PlanVersion   string          `yaml:"plan_version,omitempty"`
	UpdatedAt     string          `yaml:"updated_at,omitempty"`
	GitEnabled    bool            `yaml:"git_enabled,omitempty"`
	BaseBranch    string          `yaml:"base_branch,omitempty"`
	UpgradeNeeded bool            `yaml:"upgrade_needed,omitempty"`
	Phases        []*PhaseRuntime `yaml:"phases,omitempty"`
}

type PhaseRuntime struct {
	ID        string            `yaml:"id"`
	PlanIndex int               `yaml:"plan_index"`
	Name      string            `yaml:"name"`
	Type      string            `yaml:"type,omitempty"`
	Status    string            `yaml:"status"`
	Planner   string            `yaml:"planner_status,omitempty"`
	Sessions  []*SessionRuntime `yaml:"sessions,omitempty"`
}

type SessionRuntime struct {
	ID            string `yaml:"id"`
	Kind          string `yaml:"kind"`
	TaskName      string `yaml:"task_name,omitempty"`
	PhaseName     string `yaml:"phase_name,omitempty"`
	Goal          string `yaml:"goal"`
	Description   string `yaml:"description,omitempty"`
	Model         string `yaml:"model"`
	Status        string `yaml:"status"`
	Attempts      int    `yaml:"attempts,omitempty"`
	LastError     string `yaml:"last_error,omitempty"`
	Branch        string `yaml:"branch,omitempty"`
	BaseBranch    string `yaml:"base_branch,omitempty"`
	WorktreePath  string `yaml:"worktree_path,omitempty"`
	SessionDir    string `yaml:"session_dir,omitempty"`
	ReviewVerdict string `yaml:"review_verdict,omitempty"`
	SquashCommit  string `yaml:"squash_commit,omitempty"`
	CreatedAt     string `yaml:"created_at,omitempty"`
	UpdatedAt     string `yaml:"updated_at,omitempty"`
}

func loadRuntime(root string, p *plan.Plan) (*RuntimeState, string, error) {
	path := filepath.Join(root, "loop", "runtime", "state.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, path, err
	}

	var rt RuntimeState
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, path, err
		}
		rt = RuntimeState{Version: forgeworld.CurrentPlanVersion}
	} else if err := yaml.Unmarshal(b, &rt); err != nil {
		return nil, path, err
	}

	syncRuntimeWithPlan(&rt, root, p)
	if err := saveRuntime(path, &rt); err != nil {
		return nil, path, err
	}
	return &rt, path, nil
}

func saveRuntime(path string, rt *RuntimeState) error {
	rt.UpdatedAt = time.Now().Format(time.RFC3339)
	b, err := yaml.Marshal(rt)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func syncRuntimeWithPlan(rt *RuntimeState, root string, p *plan.Plan) {
	rt.Version = forgeworld.CurrentPlanVersion
	rt.PlanVersion = strings.TrimSpace(p.Version)
	rt.UpgradeNeeded = plan.VersionMismatch(p)
	if rt.Phases == nil {
		rt.Phases = []*PhaseRuntime{}
	}

	byIndex := make(map[int]*PhaseRuntime, len(rt.Phases))
	for _, phase := range rt.Phases {
		byIndex[phase.PlanIndex] = phase
	}

	phases := make([]*PhaseRuntime, 0, len(p.Phases))
	for idx, phase := range p.Phases {
		existing := byIndex[idx]
		if existing == nil {
			existing = &PhaseRuntime{
				ID:        fmt.Sprintf("phase-%02d-%s", idx+1, plan.TaskSlug(phase.Name)),
				PlanIndex: idx,
				Status:    phaseStatusPending,
			}
		}
		existing.Name = phase.Name
		existing.Type = plan.NormalizePhaseType(phase.Type)
		if existing.Status == "" {
			existing.Status = phaseStatusPending
		}
		completedByName := make(map[string]bool, len(phase.Tasks))
		for _, node := range phase.Tasks {
			if node.Task != nil && node.Task.Complete {
				completedByName[node.Task.Name] = true
			}
		}
		for _, sess := range existing.Sessions {
			if sess.SessionDir == "" {
				sess.SessionDir = filepath.Join(root, "loop", "sessions", sess.ID)
			}
			if sess.Status != sessionStatusMerged && completedByName[sess.TaskName] {
				sess.Status = sessionStatusMerged
				if sess.ReviewVerdict == "" {
					sess.ReviewVerdict = "approved"
				}
			}
		}
		phases = append(phases, existing)
	}
	rt.Phases = phases
}

func seedSessionsForPhase(root string, phase *PhaseRuntime, src plan.Phase) {
	if len(phase.Sessions) > 0 {
		return
	}
	phase.Planner = "seeded_from_plan_tasks"
	now := time.Now().Format(time.RFC3339)
	for idx, node := range src.Tasks {
		if node.Task == nil || node.Task.Complete {
			continue
		}
		task := node.Task
		id := fmt.Sprintf("%s-s%02d-%s", phase.ID, idx+1, plan.TaskSlug(task.Name))
		phase.Sessions = append(phase.Sessions, &SessionRuntime{
			ID:          id,
			Kind:        "task",
			TaskName:    task.Name,
			PhaseName:   phase.Name,
			Goal:        task.Name,
			Description: task.Description,
			Model:       task.Model,
			Status:      sessionStatusPlanned,
			SessionDir:  filepath.Join(root, "loop", "sessions", id),
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
}

func nextRunnableSession(rt *RuntimeState, p *plan.Plan, root string) (*PhaseRuntime, *SessionRuntime) {
	if rt.UpgradeNeeded {
		phase := ensureUpgradePhase(rt, root)
		for _, sess := range phase.Sessions {
			if sess.Status == sessionStatusPlanned || sess.Status == sessionStatusFailed {
				phase.Status = phaseStatusRunning
				return phase, sess
			}
		}
		return nil, nil
	}

	for _, phase := range rt.Phases {
		if phase == nil || phase.PlanIndex < 0 || phase.PlanIndex >= len(p.Phases) {
			continue
		}
		if phase.Status == phaseStatusCompleted {
			continue
		}
		seedSessionsForPhase(root, phase, p.Phases[phase.PlanIndex])
		phase.Status = phaseStatusRunning
		if len(phase.Sessions) == 0 {
			phase.Status = phaseStatusCompleted
			continue
		}
		allMerged := true
		for _, sess := range phase.Sessions {
			if sess.Status == sessionStatusPlanned || sess.Status == sessionStatusFailed || sess.Status == sessionStatusRejected {
				return phase, sess
			}
			if sess.Status != sessionStatusMerged {
				allMerged = false
			}
		}
		if allMerged {
			phase.Status = phaseStatusCompleted
		}
	}
	return nil, nil
}

func ensureUpgradePhase(rt *RuntimeState, root string) *PhaseRuntime {
	for _, phase := range rt.Phases {
		if phase != nil && phase.ID == "plan-upgrade" {
			if len(phase.Sessions) == 0 {
				phase.Sessions = []*SessionRuntime{newUpgradeSession(root)}
			}
			return phase
		}
	}
	phase := &PhaseRuntime{
		ID:        "plan-upgrade",
		PlanIndex: -1,
		Name:      "Actualizacion de plan",
		Type:      "upgrade",
		Status:    phaseStatusRunning,
		Planner:   "upgrade",
		Sessions:  []*SessionRuntime{newUpgradeSession(root)},
	}
	rt.Phases = append([]*PhaseRuntime{phase}, rt.Phases...)
	return phase
}

func newUpgradeSession(root string) *SessionRuntime {
	now := time.Now().Format(time.RFC3339)
	id := "plan-upgrade-session"
	return &SessionRuntime{
		ID:          id,
		Kind:        "upgrade",
		PhaseName:   "Actualizacion de plan",
		Goal:        "Actualizar plan/plan.yml a la version de metodologia actual",
		Description: "Actualizar exclusivamente plan/plan.yml y validar compatibilidad con la version actual de Forgeworld.",
		Model:       plan.ModelMedium,
		Status:      sessionStatusPlanned,
		SessionDir:  filepath.Join(root, "loop", "sessions", id),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

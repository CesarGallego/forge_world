package engine

import (
	"os"
	"path/filepath"
	"strings"
	"time"

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
)

type RuntimeState struct {
	Version   string            `yaml:"version"`
	UpdatedAt string            `yaml:"updated_at,omitempty"`
	Sessions  []*SessionRuntime `yaml:"sessions,omitempty"`
}

type SessionRuntime struct {
	ID            string   `yaml:"id"`
	Kind          string   `yaml:"kind"`
	TaskName      string   `yaml:"task_name,omitempty"`
	TaskFilename  string   `yaml:"task_filename,omitempty"`
	Goal          string   `yaml:"goal"`
	Description   string   `yaml:"description,omitempty"`
	Model         string   `yaml:"model"`
	Status        string   `yaml:"status"`
	Attempts      int      `yaml:"attempts,omitempty"`
	LastError     string   `yaml:"last_error,omitempty"`
	SessionDir    string   `yaml:"session_dir,omitempty"`
	ReviewVerdict string   `yaml:"review_verdict,omitempty"`
	Role          string   `yaml:"role,omitempty"`
	RoleHistory   []string `yaml:"role_history,omitempty"`
	Round         int      `yaml:"round,omitempty"`
	CreatedAt     string   `yaml:"created_at,omitempty"`
	UpdatedAt     string   `yaml:"updated_at,omitempty"`
}

func loadRuntime(root string, tasks []*plan.Task) (*RuntimeState, string, error) {
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
		rt = RuntimeState{Version: "3"}
	} else if err := yaml.Unmarshal(b, &rt); err != nil {
		return nil, path, err
	}

	syncRuntimeWithTasks(&rt, root, tasks)
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

func normalizeSessionStatus(status string) string {
	switch status {
	case "review_pending", "approved":
		return sessionStatusFailed
	default:
		return status
	}
}

func syncRuntimeWithTasks(rt *RuntimeState, root string, tasks []*plan.Task) {
	rt.Version = "3"
	if rt.Sessions == nil {
		rt.Sessions = []*SessionRuntime{}
	}

	// Preserve known meta sessions (e.g. fase0) at the front so they survive task sync.
	var nonTaskSessions []*SessionRuntime
	for _, sess := range rt.Sessions {
		if sess.Kind == "fase0" {
			nonTaskSessions = append(nonTaskSessions, sess)
		}
	}

	byID := make(map[string]*SessionRuntime, len(rt.Sessions))
	for _, sess := range rt.Sessions {
		byID[sess.ID] = sess
	}

	now := time.Now().Format(time.RFC3339)
	newSessions := make([]*SessionRuntime, 0, len(tasks))
	for _, task := range tasks {
		id := taskFilenameToSessionID(task.Filename)
		if existing, ok := byID[id]; ok {
			existing.Status = normalizeSessionStatus(existing.Status)
			if task.Complete && existing.Status != sessionStatusMerged {
				existing.Status = sessionStatusMerged
				if existing.ReviewVerdict == "" {
					existing.ReviewVerdict = "approved"
				}
			}
			if existing.SessionDir == "" {
				existing.SessionDir = filepath.Join(root, "loop", "sessions", id)
			}
			newSessions = append(newSessions, existing)
		} else {
			newSessions = append(newSessions, &SessionRuntime{
				ID:           id,
				Kind:         "task",
				TaskName:     task.Name,
				TaskFilename: task.Filename,
				Goal:         task.Name,
				Model:        task.Model,
				Status:       sessionStatusPlanned,
				SessionDir:   filepath.Join(root, "loop", "sessions", id),
				CreatedAt:    now,
				UpdatedAt:    now,
			})
		}
	}
	rt.Sessions = append(nonTaskSessions, newSessions...)
}

// taskFilenameToSessionID converts "001-crear-api.md" to "s001-crear-api"
func taskFilenameToSessionID(filename string) string {
	name := strings.TrimSuffix(filename, ".md")
	return "s" + name
}

func nextRunnableSessionFlat(rt *RuntimeState) *SessionRuntime {
	for _, sess := range rt.Sessions {
		if sess.Status == sessionStatusPlanned || sess.Status == sessionStatusFailed || sess.Status == sessionStatusRejected {
			return sess
		}
	}
	return nil
}

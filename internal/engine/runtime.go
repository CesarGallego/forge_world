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
	Version    string            `yaml:"version"`
	UpdatedAt  string            `yaml:"updated_at,omitempty"`
	GitEnabled bool              `yaml:"git_enabled,omitempty"`
	BaseBranch string            `yaml:"base_branch,omitempty"`
	Sessions   []*SessionRuntime `yaml:"sessions,omitempty"`
}

type SessionRuntime struct {
	ID            string `yaml:"id"`
	Kind          string `yaml:"kind"`
	TaskName      string `yaml:"task_name,omitempty"`
	TaskFilename  string `yaml:"task_filename,omitempty"`
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

func syncRuntimeWithTasks(rt *RuntimeState, root string, tasks []*plan.Task) {
	rt.Version = "3"
	if rt.Sessions == nil {
		rt.Sessions = []*SessionRuntime{}
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
	rt.Sessions = newSessions
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

package gitflow

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Worktree struct {
	Path   string
	Branch string
}

func IsGitRepo(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = root
	return cmd.Run() == nil
}

func CurrentBranch(root string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("no se pudo detectar rama base: %w (%s)", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func Create(root, key string) (*Worktree, error) {
	if !IsGitRepo(root) {
		return nil, errors.New("parallel requiere un repositorio git")
	}
	branch := sanitize(fmt.Sprintf("forgeworld/%s/%d", key, time.Now().Unix()))
	path := filepath.Join(root, ".forgeworld-worktrees", sanitize(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "worktree", "add", "-b", branch, path)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("fallo creando worktree: %w (%s)", err, string(out))
	}
	return &Worktree{Path: path, Branch: branch}, nil
}

func Cleanup(root string, wt *Worktree) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", wt.Path)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fallo limpiando worktree: %w (%s)", err, string(out))
	}
	cmd = exec.Command("git", "branch", "-D", wt.Branch)
	cmd.Dir = root
	_ = cmd.Run()
	return nil
}

func Merge(root, base string, branches []string) error {
	co := exec.Command("git", "checkout", base)
	co.Dir = root
	if out, err := co.CombinedOutput(); err != nil {
		return fmt.Errorf("fallo checkout base: %w (%s)", err, string(out))
	}
	for _, b := range branches {
		cmd := exec.Command("git", "merge", "--no-ff", "--no-edit", b)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			abort := exec.Command("git", "merge", "--abort")
			abort.Dir = root
			_ = abort.Run()
			return fmt.Errorf("merge conflict con %s: %w (%s)", b, err, string(out))
		}
	}
	return nil
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

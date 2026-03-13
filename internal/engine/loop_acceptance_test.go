package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoopOncePersistsRejectedSessionState(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "reject")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce failed unexpectedly: %v", err)
	}

	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Status != sessionStatusRejected {
		t.Fatalf("expected rejected status, got %q", sess.Status)
	}
	if sess.ReviewVerdict != "rejected" {
		t.Fatalf("expected rejected verdict, got %q", sess.ReviewVerdict)
	}
	if sess.WorktreePath == "" {
		t.Fatalf("expected retained worktree path")
	}
	if _, err := os.Stat(sess.WorktreePath); err != nil {
		t.Fatalf("expected worktree to remain on disk: %v", err)
	}
}

func TestLoopOnceRetriesRejectedSessionWithErrorPrompt(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "reject")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("first LoopOnce failed unexpectedly: %v", err)
	}

	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("second LoopOnce failed: %v", err)
	}

	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Attempts != 2 {
		t.Fatalf("expected 2 attempts after recovery, got %d", sess.Attempts)
	}
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "used-error-prompt")); err != nil {
		t.Fatalf("expected recovery attempt to use error prompt: %v", err)
	}
	feedback := readFile(t, filepath.Join(sess.SessionDir, "feedback.md"))
	if !strings.Contains(feedback, "estado_anterior: rejected") {
		t.Fatalf("expected feedback.md to capture previous rejected status: %s", feedback)
	}
}

func TestLoopOncePersistsMergeSectionAndCleansApprovedWorktree(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "approve")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce failed: %v", err)
	}

	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Status != sessionStatusMerged {
		t.Fatalf("expected merged status, got %q", sess.Status)
	}
	if sess.ReviewVerdict != "approved" {
		t.Fatalf("expected approved verdict, got %q", sess.ReviewVerdict)
	}
	if !strings.Contains(st.LastRuns[0].Stdout, "=== MERGE ===") {
		t.Fatalf("expected merge block in last run stdout: %s", st.LastRuns[0].Stdout)
	}
	runLog := readFile(t, filepath.Join(root, "loop", "runs", st.LastRuns[0].ID, "stdout.log"))
	if !strings.Contains(runLog, "=== MERGE ===") {
		t.Fatalf("expected merge block in persisted stdout.log: %s", runLog)
	}
	if !strings.Contains(runLog, "forgeworld(merge): Crear archivo de prueba") {
		t.Fatalf("expected squash commit message in stdout.log: %s", runLog)
	}
	if sess.WorktreePath == "" {
		t.Fatalf("expected worktree path to be recorded")
	}
	if _, err := os.Stat(sess.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected approved worktree to be removed, got err=%v", err)
	}

	logOut := runGit(t, root, "log", "--oneline", "-n", "1")
	if !strings.Contains(logOut, "forgeworld(merge): Crear archivo de prueba") {
		t.Fatalf("expected squash merge commit, got %s", logOut)
	}
	branches := runGit(t, root, "branch", "--list", "forgeworld/*")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("expected no leftover forgeworld branches, got %q", branches)
	}

	// Assert task file is marked complete after merge
	taskContent := readFile(t, filepath.Join(root, "plan", "tasks", "001-crear-archivo-de-prueba.md"))
	if !strings.Contains(taskContent, "complete: true") {
		t.Fatalf("expected task file to have complete: true after merge, got:\n%s", taskContent)
	}
}

func TestLoopOnceMissingCompletionMarkerCanStillMergeWhenReviewApproves(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "approve")
	if err := os.WriteFile(filepath.Join(root, ".omega-mode"), []byte("missing-marker"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce failed: %v", err)
	}

	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Status != sessionStatusMerged {
		t.Fatalf("expected merged status, got %q", sess.Status)
	}
	if sess.ReviewVerdict != "approved" {
		t.Fatalf("expected approved verdict, got %q", sess.ReviewVerdict)
	}
	if sess.LastError != "" {
		t.Fatalf("expected cleared last_error after approved protocol review, got %q", sess.LastError)
	}
	reviewPrompt := readFile(t, filepath.Join(sess.SessionDir, "review.md"))
	if !strings.Contains(reviewPrompt, "Omega termino sin emitir FORGEWORLD_TASK_COMPLETE") {
		t.Fatalf("expected protocol incident in review prompt: %s", reviewPrompt)
	}
}

func TestLoopOnceMissingCompletionMarkerStaysFailedWhenReviewRejects(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "reject")
	if err := os.WriteFile(filepath.Join(root, ".omega-mode"), []byte("missing-marker"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce failed unexpectedly: %v", err)
	}

	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Status != sessionStatusFailed {
		t.Fatalf("expected failed status, got %q", sess.Status)
	}
	if sess.ReviewVerdict != "rejected" {
		t.Fatalf("expected rejected verdict, got %q", sess.ReviewVerdict)
	}
	if !strings.Contains(sess.LastError, "omega no confirmo finalizacion; review indico continuar la sesion") {
		t.Fatalf("expected combined protocol/review error, got %q", sess.LastError)
	}
	if sess.WorktreePath == "" {
		t.Fatalf("expected retained worktree path")
	}
	if _, err := os.Stat(sess.WorktreePath); err != nil {
		t.Fatalf("expected worktree to remain on disk: %v", err)
	}
}

func TestLoopOnceRecreatesWorktreeWhenDirectoryIsMissing(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "approve")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	// First run to create the worktree.
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("first LoopOnce failed: %v", err)
	}
	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Status != sessionStatusMerged {
		t.Fatalf("expected merged, got %q", sess.Status)
	}

	// Simulate a new incomplete session by injecting a failed session with a stale worktree path.
	for _, s := range rt.Sessions {
		if s.Goal == "Crear archivo de prueba" {
			s.Status = sessionStatusFailed
			s.WorktreePath = filepath.Join(root, "loop", "worktrees", "gone-worktree")
			s.Branch = "forgeworld/gone-branch"
			s.Attempts = 1
		}
	}
	if err := saveRuntime(filepath.Join(root, "loop", "runtime", "state.yml"), &rt); err != nil {
		t.Fatalf("saveRuntime failed: %v", err)
	}

	st2, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState after inject failed: %v", err)
	}
	// Should not fail due to missing worktree — it must recreate it.
	if err := st2.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce with stale worktree failed: %v", err)
	}
}

func TestLoopOnceEscalatesModelOnEachFailure(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "reject")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Attempt 1: small -> fail -> escalate to medium
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce #1 failed unexpectedly: %v", err)
	}
	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Attempts != 1 {
		t.Fatalf("expected 1 attempt after #1, got %d", sess.Attempts)
	}
	if sess.Model != "medium" {
		t.Fatalf("expected model escalated to medium after attempt 1, got %q", sess.Model)
	}

	// Attempt 2: medium -> fail -> escalate to large
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce #2 failed unexpectedly: %v", err)
	}
	rt = readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess = runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Attempts != 2 {
		t.Fatalf("expected 2 attempts after #2, got %d", sess.Attempts)
	}
	if sess.Model != "large" {
		t.Fatalf("expected model escalated to large after attempt 2, got %q", sess.Model)
	}

	// Attempt 3: large -> fail -> can't escalate, attempts>=3 -> stop.md
	if err := st.LoopOnce(context.Background()); err == nil {
		t.Fatalf("expected LoopOnce #3 to return error after exhausting escalation")
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err != nil {
		t.Fatalf("expected loop/stop.md to be created after exhausting escalation: %v", err)
	}
	stopContent, _ := os.ReadFile(filepath.Join(root, "loop", "stop.md"))
	if !strings.Contains(string(stopContent), "intervencion manual") {
		t.Fatalf("expected stop.md to mention manual intervention, got: %s", stopContent)
	}
}

func setupLoopTestRepo(t *testing.T, reviewMode string) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	writePrompts(t, home)
	writeExecutor(t, root)
	writeConfig(t, root)
	writeTaskFiles(t, root)
	if reviewMode != "" {
		if err := os.WriteFile(filepath.Join(root, ".review-mode"), []byte(reviewMode), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	initGitRepo(t, root)
	return root, home
}

func writePrompts(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "forgeworld")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prompts := map[string]string{
		"alpha.md": "alpha {{task_name}}",
		"error.md": "RECOVERY_PROMPT {{task_name}} {{feedback_file}}",
		"review.md": "review {{session_goal}}\n{{diff_summary}}",
	}
	for name, body := range prompts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
}

func writeExecutor(t *testing.T, root string) {
	t.Helper()
	script := `#!/usr/bin/env bash
set -euo pipefail
prompt=$1
root=` + "`cd \"$(dirname \"$prompt\")/../../..\" && pwd`" + `
mode_file="$root/.review-mode"
mode=approve
if [ -f "$mode_file" ]; then
  mode=$(tr -d "\r\n" < "$mode_file")
fi
omega_mode_file="$root/.omega-mode"
omega_mode=normal
if [ -f "$omega_mode_file" ]; then
  omega_mode=$(tr -d "\r\n" < "$omega_mode_file")
fi
case "$(basename "$prompt")" in
  prompt.md)
    if grep -q "RECOVERY_PROMPT" "$prompt"; then
      touch "$(dirname "$prompt")/used-error-prompt"
    fi
    printf "TASK_OMEGA\n"
    ;;
  omega.md)
    printf "ok\n" > "$PWD/test-output.txt"
    if [ "$omega_mode" != "missing-marker" ]; then
      printf "FORGEWORLD_TASK_COMPLETE\n"
    fi
    ;;
  review.md)
    if [ "$mode" = "reject" ]; then
      printf "REJECTED\nreview reject forced\n"
    else
      printf "APPROVED\nreview approve forced\n"
    fi
    ;;
  *)
    printf "unexpected prompt %s\n" "$prompt" >&2
    exit 1
    ;;
esac
`
	path := filepath.Join(root, "fake-executor.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, root string) {
	t.Helper()
	cfg := `executor:
  command: bash
  args:
    - -lc
    - ./fake-executor.sh "{{prompt}}"
models:
  small: fake-small
  medium: fake-medium
  large: fake-large
`
	if err := os.WriteFile(filepath.Join(root, ".forgeworld.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTaskFiles(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "plan", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nmodel: small\ncomplete: false\n---\n# Crear archivo de prueba\n\nCrear test-output.txt con contenido ok.\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "tasks", "001-crear-archivo-de-prueba.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initGitRepo(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "tester")
	runGit(t, root, "config", "user.email", "tester@example.com")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "baseline")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func readRuntimeState(t *testing.T, path string) RuntimeState {
	t.Helper()
	var rt RuntimeState
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(b, &rt); err != nil {
		t.Fatal(err)
	}
	return rt
}

func runtimeSessionByGoal(t *testing.T, rt *RuntimeState, goal string) *SessionRuntime {
	t.Helper()
	for _, sess := range rt.Sessions {
		if sess.Goal == goal {
			return sess
		}
	}
	t.Fatalf("session with goal %q not found", goal)
	return nil
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

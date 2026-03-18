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

// TestLoopOncePersistsFailedSessionState verifies that when omega fails, the session
// is marked failed and the worktree is retained for recovery.
func TestLoopOncePersistsFailedSessionState(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "fail")

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
	if sess.WorktreePath == "" {
		t.Fatalf("expected retained worktree path")
	}
	if _, err := os.Stat(sess.WorktreePath); err != nil {
		t.Fatalf("expected worktree to remain on disk: %v", err)
	}
}

func TestLoopOnceRetriesFailedSessionWithErrorPrompt(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "fail")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("first LoopOnce failed unexpectedly: %v", err)
	}

	// Switch to approve mode so the second run succeeds
	if err := os.WriteFile(filepath.Join(root, ".review-mode"), []byte("approve"), 0o644); err != nil {
		t.Fatal(err)
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
	if !strings.Contains(feedback, "estado_anterior: failed") {
		t.Fatalf("expected feedback.md to capture previous failed status: %s", feedback)
	}
	// Verify role log files from second run
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "000-error.log")); err != nil {
		t.Fatalf("expected 000-error.log from recovery run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "001-omega.log")); err != nil {
		t.Fatalf("expected 001-omega.log from recovery run: %v", err)
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

	// Verify role log files
	for _, logFile := range []string{"001-omega.log", "002-judge.log", "003-merge.log", "004-done.log"} {
		if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", logFile)); err != nil {
			t.Fatalf("expected role log %s: %v", logFile, err)
		}
	}

	// WorktreePath is cleared by cleanupWorktree so a second merge call on the
	// same session correctly skips via the sess.Branch == "" early return.
	if sess.WorktreePath != "" {
		t.Fatalf("expected worktree path to be cleared after cleanup, got %q", sess.WorktreePath)
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

func TestLoopOnceRoleChainHappyPath(t *testing.T) {
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

	// Verify RoleHistory contains the expected roles
	expectedRoles := []string{"omega", "judge", "merge", "done"}
	if len(sess.RoleHistory) != len(expectedRoles) {
		t.Fatalf("expected RoleHistory %v, got %v", expectedRoles, sess.RoleHistory)
	}
	for i, r := range expectedRoles {
		if sess.RoleHistory[i] != r {
			t.Fatalf("RoleHistory[%d]: expected %q, got %q", i, r, sess.RoleHistory[i])
		}
	}

	// Verify role log files exist
	rolesDir := filepath.Join(sess.SessionDir, "roles")
	for _, logFile := range []string{"000-alpha.log", "001-omega.log", "002-judge.log", "003-merge.log", "004-done.log"} {
		if _, err := os.Stat(filepath.Join(rolesDir, logFile)); err != nil {
			t.Fatalf("expected role log %s: %v", logFile, err)
		}
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
	root, _ := setupLoopTestRepo(t, "fail")

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

func TestLoopOnceProjectLocalRoleTakesPrecedence(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "approve")

	// Write a project-local judge role that contains LOCAL_JUDGE marker.
	// The executor checks for this marker and approves immediately.
	rolesDir := filepath.Join(root, "loop", "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localJudge := "LOCAL_JUDGE approve {{task_name}}\nFORGEWORLD_NEXT: merge"
	if err := os.WriteFile(filepath.Join(rolesDir, "judge.md"), []byte(localJudge), 0o644); err != nil {
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
		t.Fatalf("expected merged status (local judge used), got %q", sess.Status)
	}

	// Verify the session dir judge.md was rendered from local role
	judgePromptContent := readFile(t, filepath.Join(sess.SessionDir, "judge.md"))
	if !strings.Contains(judgePromptContent, "LOCAL_JUDGE") {
		t.Fatalf("expected local judge prompt to be used, judge.md content: %s", judgePromptContent)
	}
}

func TestLoopOnceBackwardCompatOldStatuses(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "approve")

	// Manually create a state.yml with review_pending status (old format)
	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	// Inject review_pending into runtime
	for _, sess := range st.Runtime.Sessions {
		if sess.Goal == "Crear archivo de prueba" {
			sess.Status = "review_pending"
		}
	}
	if err := st.saveRuntime(); err != nil {
		t.Fatalf("saveRuntime failed: %v", err)
	}

	// Reload state — normalizeSessionStatus should convert review_pending → failed
	st2, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState after inject failed: %v", err)
	}
	sess2 := runtimeSessionByGoal(t, st2.Runtime, "Crear archivo de prueba")
	if sess2.Status != sessionStatusFailed {
		t.Fatalf("expected review_pending to be normalized to failed, got %q", sess2.Status)
	}

	// Run should succeed since mode is "approve"
	if err := st2.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce with normalized status failed: %v", err)
	}
	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Status != sessionStatusMerged {
		t.Fatalf("expected merged after re-run, got %q", sess.Status)
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
		"alpha.md":      "alpha {{task_name}} {{session_id}} {{session_dir}} {{available_roles}}",
		"error.md":      "RECOVERY_PROMPT {{task_name}} {{feedback_file}}",
		"review.md":     "review {{task_name}}",
		"judge.md":      "judge {{task_name}} {{diff_summary}}",
		"merge.md":      "merge {{task_name}} {{merge_result}}",
		"done.md":       "done {{task_name}} {{merge_result}}",
		"plan.md":       "plan {{task_name}} {{available_roles}}",
		"crit-error.md": "crit-error {{task_name}} {{previous_role}} {{session_dir}}",
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
case "$(basename "$prompt" .md)" in
  prompt)
    if grep -q "RECOVERY_PROMPT" "$prompt"; then
      touch "$(dirname "$prompt")/used-error-prompt"
    fi
    printf "TASK_OMEGA\n"
    ;;
  omega)
    if [ "$mode" = "fail" ]; then
      printf "omega forcibly failed\n" >&2
      exit 1
    fi
    if [ "$mode" = "nochain" ]; then
      printf "working but no signal\n"
      exit 0
    fi
    if [ "$mode" = "empty" ]; then
      # Simulate task already done: no file changes, just signal judge
      printf "task already done, no changes\n"
      printf "FORGEWORLD_NEXT: judge\n"
      exit 0
    fi
    printf "ok\n" > "$PWD/test-output.txt"
    printf "work done\n"
    printf "FORGEWORLD_NEXT: judge\n"
    ;;
  judge)
    if grep -q "LOCAL_JUDGE" "$prompt" 2>/dev/null; then
      printf "local judge approved\n"
      printf "FORGEWORLD_NEXT: merge\n"
    elif [ "$mode" = "reject" ]; then
      printf "judge rejected\n"
      printf "FORGEWORLD_NEXT: omega\n"
    elif [ "$mode" = "nochain" ]; then
      printf "judging but no signal\n"
      exit 0
    else
      printf "judge approved\n"
      printf "FORGEWORLD_NEXT: merge\n"
    fi
    ;;
  merge)
    printf "merge ok\n"
    printf "FORGEWORLD_NEXT: done\n"
    ;;
  done)
    printf "done ok\n"
    printf "FORGEWORLD_NEXT: done\n"
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

// TestLoopOnceEmptyDiffMergesCleanly verifies that when the task is already
// complete (no changes in the worktree vs base), the merge step succeeds
// without failing: it detects the empty diff, skips the commit, cleans up
// the worktree, and the session reaches merged status via the done role.
func TestLoopOnceEmptyDiffMergesCleanly(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "empty")

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
		t.Fatalf("expected merged status, got %q (last_error: %s)", sess.Status, sess.LastError)
	}
	if sess.Attempts != 1 {
		t.Fatalf("expected single attempt, got %d", sess.Attempts)
	}
	// No squash commit because there were no changes to commit
	if sess.SquashCommit != "" {
		t.Fatalf("expected no squash commit for empty diff, got %q", sess.SquashCommit)
	}
	// Worktree should be cleaned up
	if _, err := os.Stat(sess.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree cleaned up, got err=%v", err)
	}
	// No stop.md should exist
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err == nil {
		t.Fatal("stop.md must not exist after clean empty-diff merge")
	}
}

// TestLoopOnceMaxIterationsEscalatesWithoutStopMd verifies that when the role
// chain exhausts max iterations, no stop.md is written and the model is
// escalated so that higher-tier models get a chance to break the loop.
// Only after all model tiers are exhausted should stop.md be created.
func TestLoopOnceMaxIterationsEscalatesWithoutStopMd(t *testing.T) {
	orig := maxRoleChainIterations
	maxRoleChainIterations = 2
	defer func() { maxRoleChainIterations = orig }()

	root, _ := setupLoopTestRepo(t, "nochain")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Attempt 1 (small): max iterations → no stop.md, escalate to medium
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce #1 should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err == nil {
		t.Fatal("stop.md must NOT be written after first max-iterations failure")
	}
	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Model != "medium" {
		t.Fatalf("expected model escalated to medium, got %q", sess.Model)
	}

	// Attempt 2 (medium): max iterations → no stop.md, escalate to large
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce #2 should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err == nil {
		t.Fatal("stop.md must NOT be written after second max-iterations failure")
	}
	rt = readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess = runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Model != "large" {
		t.Fatalf("expected model escalated to large, got %q", sess.Model)
	}

	// Attempt 3 (large): max iterations → now stop.md IS written (all tiers exhausted)
	if err := st.LoopOnce(context.Background()); err == nil {
		t.Fatal("LoopOnce #3 should return error after exhausting all model tiers")
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err != nil {
		t.Fatalf("expected stop.md after exhausting all model tiers: %v", err)
	}
}

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

// TestLoopOncePersistsFailedSessionState verifies that when alpha fails, the session
// is marked failed.
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
	// Verify role log files from second run (round-0 subdir)
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "round-0", "000-error.log")); err != nil {
		t.Fatalf("expected round-0/000-error.log from recovery run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "round-0", "001-task.log")); err != nil {
		t.Fatalf("expected round-0/001-task.log from recovery run: %v", err)
	}
}

func TestLoopOnceCompletesSessionWithoutGitMerge(t *testing.T) {
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

	// Verify role log files exist for round-0 (alpha + omega) and round-1 (alpha done)
	for _, logFile := range []string{
		filepath.Join("round-0", "000-alpha.log"),
		filepath.Join("round-0", "001-task.log"),
		filepath.Join("round-1", "000-alpha.log"),
	} {
		if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", logFile)); err != nil {
			t.Fatalf("expected role log %s: %v", logFile, err)
		}
	}

	// Assert plan.md marks task as complete (checkbox checked).
	planContent := readFile(t, filepath.Join(root, "plan", "plan.md"))
	if !strings.Contains(planContent, "[x] crear-archivo-de-prueba") {
		t.Fatalf("expected plan.md to mark task complete, got:\n%s", planContent)
	}
}

// TestLoopOnceAlphaWritesDoneMd verifies that when alpha writes only done.md, the session
// is merged in a single round without executing any omega files.
func TestLoopOnceAlphaWritesDoneMd(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "done")

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
	if sess.Round != 0 {
		t.Fatalf("expected round=0 (no omega ran), got %d", sess.Round)
	}

	// Alpha log should exist, no omega logs
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "round-0", "000-alpha.log")); err != nil {
		t.Fatalf("expected round-0/000-alpha.log: %v", err)
	}

	// Task should be marked complete in plan.md.
	planContent := readFile(t, filepath.Join(root, "plan", "plan.md"))
	if !strings.Contains(planContent, "[x] crear-archivo-de-prueba") {
		t.Fatalf("expected plan.md to mark task complete, got:\n%s", planContent)
	}
}

// TestLoopOnceMultiOmegaParallel verifies that alpha can write multiple omega files
// which are executed in parallel, then alpha re-evaluates and writes done.md.
func TestLoopOnceMultiOmegaParallel(t *testing.T) {
	root, _ := setupLoopTestRepo(t, "multi-omega")

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

	// Round 0 should have both omega logs
	for _, logFile := range []string{
		filepath.Join("round-0", "001-task.log"),
		filepath.Join("round-0", "002-task.log"),
	} {
		if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", logFile)); err != nil {
			t.Fatalf("expected role log %s: %v", logFile, err)
		}
	}

	// Archived files should exist
	for _, archiveFile := range []string{"001-task.md", "002-task.md"} {
		if _, err := os.Stat(filepath.Join(sess.SessionDir, "omega-archive", "round-0", archiveFile)); err != nil {
			t.Fatalf("expected archived omega file %s: %v", archiveFile, err)
		}
	}
}

// TestLoopOnceAlphaReEvaluatesAfterOmega verifies the 2-round flow:
// alpha writes omega files → omega runs → alpha re-evaluates → writes done.md → merged.
func TestLoopOnceAlphaReEvaluatesAfterOmega(t *testing.T) {
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
	// Should have gone through at least 1 round (round=1 after increment)
	if sess.Round < 1 {
		t.Fatalf("expected at least 1 completed round, got %d", sess.Round)
	}

	// Round-0 omega log should exist
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "round-0", "001-task.log")); err != nil {
		t.Fatalf("expected round-0/001-task.log: %v", err)
	}
	// Round-1 alpha (re-evaluation) log should exist
	if _, err := os.Stat(filepath.Join(sess.SessionDir, "roles", "round-1", "000-alpha.log")); err != nil {
		t.Fatalf("expected round-1/000-alpha.log: %v", err)
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

// TestLoopOnceMaxIterationsEscalatesWithoutStopMd verifies that when the omega loop
// exhausts max rounds, no stop.md is written and the model is escalated.
// Only after all model tiers are exhausted should stop.md be created.
func TestLoopOnceMaxIterationsEscalatesWithoutStopMd(t *testing.T) {
	orig := maxOmegaRounds
	maxOmegaRounds = 2
	defer func() { maxOmegaRounds = orig }()

	root, _ := setupLoopTestRepo(t, "loop")

	st, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Attempt 1 (small): max rounds → no stop.md, escalate to medium
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce #1 should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err == nil {
		t.Fatal("stop.md must NOT be written after first max-rounds failure")
	}
	rt := readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess := runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Model != "medium" {
		t.Fatalf("expected model escalated to medium, got %q", sess.Model)
	}

	// Attempt 2 (medium): max rounds → no stop.md, escalate to large
	if err := st.LoopOnce(context.Background()); err != nil {
		t.Fatalf("LoopOnce #2 should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err == nil {
		t.Fatal("stop.md must NOT be written after second max-rounds failure")
	}
	rt = readRuntimeState(t, filepath.Join(root, "loop", "runtime", "state.yml"))
	sess = runtimeSessionByGoal(t, &rt, "Crear archivo de prueba")
	if sess.Model != "large" {
		t.Fatalf("expected model escalated to large, got %q", sess.Model)
	}

	// Attempt 3 (large): max rounds → now stop.md IS written (all tiers exhausted)
	if err := st.LoopOnce(context.Background()); err == nil {
		t.Fatal("LoopOnce #3 should return error after exhausting all model tiers")
	}
	if _, err := os.Stat(filepath.Join(root, "loop", "stop.md")); err != nil {
		t.Fatalf("expected stop.md after exhausting all model tiers: %v", err)
	}
}

func setupLoopTestRepo(t *testing.T, reviewMode string) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrompts(t, root)
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

func writePrompts(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "loop", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prompts := map[string]string{
		"alpha.md": "alpha {{task_name}} {{session_id}} {{session_dir}} {{omega_dir}}",
		"error.md": "RECOVERY_PROMPT {{task_name}} {{feedback_file}}",
		"fase0.md": "fase0 {{task_name}} {{omega_dir}}",
	}
	for name, body := range prompts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeExecutor(t *testing.T, root string) {
	t.Helper()
	// The executor is called as: fake-executor.sh "{{prompt}}" "{{task_dir}}"
	// where task_dir = session dir = <root>/loop/sessions/<sess_id>
	// Alpha (prompt.md): writes files to omega/ dir based on mode and round counter.
	// Omega files (any other .md): just do work and exit 0.
	script := `#!/usr/bin/env bash
set -euo pipefail
prompt=$1
taskdir=${2:-}

# Derive root from task_dir (session_dir = <root>/loop/sessions/<sess_id>)
# Fall back to deriving from prompt path for alpha (they're in the same dir)
if [ -n "$taskdir" ]; then
  root=$(cd "$taskdir/../../.." && pwd)
else
  root=$(cd "$(dirname "$prompt")/../../.." && pwd)
fi

mode_file="$root/.review-mode"
mode=approve
if [ -f "$mode_file" ]; then
  mode=$(tr -d "\r\n" < "$mode_file")
fi

basename=$(basename "$prompt" .md)
case "$basename" in
  prompt)
    sessdir=$(dirname "$prompt")
    omegadir="$sessdir/omega"
    mkdir -p "$omegadir"

    # Track which alpha round we're on within this session attempt
    roundfile="$sessdir/alpha-round"
    round=0
    if [ -f "$roundfile" ]; then
      round=$(cat "$roundfile")
    fi

    if grep -q "RECOVERY_PROMPT" "$prompt"; then
      touch "$sessdir/used-error-prompt"
    fi

    # Fail mode exits before incrementing the round counter so that
    # a subsequent recovery attempt starts at round 0.
    if [ "$mode" = "fail" ]; then
      printf "alpha failed\n" >&2
      exit 1
    fi

    echo $((round + 1)) > "$roundfile"

    case "$mode" in
      done)
        printf "Task complete\n" > "$omegadir/done.md"
        ;;
      approve)
        if [ "$round" -ge 1 ]; then
          printf "Task complete\n" > "$omegadir/done.md"
        else
          printf "Do the work\n" > "$omegadir/001-task.md"
        fi
        ;;
      multi-omega)
        if [ "$round" -ge 1 ]; then
          printf "Task complete\n" > "$omegadir/done.md"
        else
          printf "Task 1\n" > "$omegadir/001-task.md"
          printf "Task 2\n" > "$omegadir/002-task.md"
        fi
        ;;
      loop)
        # Always write omega file, never done.md (tests max iterations)
        printf "Task\n" > "$omegadir/001-task.md"
        ;;
      omega-fail)
        # Alpha succeeds; omega will fail on first round
        if [ "$round" -ge 1 ]; then
          printf "Task complete\n" > "$omegadir/done.md"
        else
          printf "Do the work\n" > "$omegadir/001-task.md"
        fi
        ;;
    esac
    ;;
  *)
    # Omega file execution: do the work
    # For omega-fail mode, fail on round 0 (check round file via taskdir)
    if [ "$mode" = "omega-fail" ] && [ -n "$taskdir" ]; then
      roundfile="$taskdir/alpha-round"
      round=0
      if [ -f "$roundfile" ]; then
        round=$(cat "$roundfile")
      fi
      if [ "$round" -le 1 ]; then
        printf "omega failed\n" >&2
        exit 1
      fi
    fi
    printf "ok\n" > "$PWD/test-output.txt"
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
    - ./fake-executor.sh "{{prompt}}" "{{task_dir}}"
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
	// plan.md with fase0 already done so acceptance tests focus on task execution.
	planMd := "---\nfase0: true\n---\n# Plan\n\n- [ ] crear-archivo-de-prueba\n"
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.md"), []byte(planMd), 0o644); err != nil {
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

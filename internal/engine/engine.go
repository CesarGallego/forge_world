package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"forgeworld"
	"forgeworld/internal/config"
	"forgeworld/internal/plan"

	"gopkg.in/yaml.v3"
)

type RunRecord struct {
	ID       string
	TaskName string
	Stdout   string
	Stderr   string
	Code     int
	Model    string
	Err      error
}

const omegaCompletionMarker = "FORGEWORLD_TASK_COMPLETE"

type State struct {
	Root         string
	Config       *config.Config
	PlanPath     string
	Plan         *plan.Plan
	RuntimePath  string
	Runtime      *RuntimeState
	StatusLine   string
	currentPhase string

	mu          sync.RWMutex
	LastRuns    []RunRecord
	activeRuns  map[string]*RunRecord
	activeOrder []string
}

func LoadState(root string) (*State, error) {
	cfg, err := config.LoadLocal(root)
	if err != nil {
		return nil, err
	}
	p, path, err := plan.Load(root)
	if err != nil {
		return nil, err
	}
	changedPhase0 := plan.EnsurePhase0(p)
	changedCompletion := plan.ReconcileCompletion(p)
	if changedPhase0 || changedCompletion {
		if err := plan.Save(path, p); err != nil {
			return nil, err
		}
	}
	rt, rtPath, err := loadRuntime(root, p)
	if err != nil {
		return nil, err
	}
	return &State{Root: root, Config: cfg, PlanPath: path, Plan: p, RuntimePath: rtPath, Runtime: rt}, nil
}

func (s *State) Tree(selectedTask string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan v%s | Runtime v%s\n", valueOrDash(strings.TrimSpace(s.Plan.Version)), s.Runtime.Version)
	if s.Runtime.UpgradeNeeded {
		fmt.Fprintf(&b, "[!] upgrade de plan requerido -> %s\n", forgeworld.CurrentPlanVersion)
	}
	for _, phase := range s.Runtime.Phases {
		if phase == nil {
			continue
		}
		mark := "[ ]"
		if phase.Status == phaseStatusCompleted {
			mark = "[x]"
		} else if phase.Status == phaseStatusRunning {
			mark = "[>]"
		}
		fmt.Fprintf(&b, "%s %s\n", mark, phase.Name)
		for _, sess := range phase.Sessions {
			tm := sessionMarker(sess.Status)
			if strings.TrimSpace(selectedTask) != "" && sess.ID == selectedTask {
				tm = "[*]"
			}
			fmt.Fprintf(&b, "  %s %s (%s)\n", tm, sess.Goal, sess.Model)
		}
	}
	return b.String()
}

func sessionMarker(status string) string {
	switch status {
	case sessionStatusMerged:
		return "[x]"
	case sessionStatusRunning, sessionStatusReviewPending, sessionStatusApproved:
		return "[>]"
	case sessionStatusRejected, sessionStatusFailed:
		return "[!]"
	default:
		return "[ ]"
	}
}

func (s *State) LoopOnce(ctx context.Context) error {
	s.debugf("loop_once.start status_line=%q summary=%s", s.StatusLine, s.runtimeSummary())
	if err := s.reloadState(); err != nil {
		s.debugf("loop_once.reload_state.error err=%q", err)
		return err
	}
	s.debugf("loop_once.after_reload summary=%s", s.runtimeSummary())
	if hasStop(s.Root) {
		s.debugf("loop_once.stop_present")
		return fmt.Errorf("se encontro loop/stop.md; revisa bloqueo antes de continuar")
	}

	phaseRun, sess := nextRunnableSession(s.Runtime, s.Plan, s.Root)
	if sess == nil {
		s.StatusLine = "Plan completado."
		s.debugf("loop_once.no_runnable_session summary=%s", s.runtimeSummary())
		if err := s.saveRuntime(); err != nil {
			s.debugf("loop_once.save_runtime.error err=%q", err)
			return err
		}
		return nil
	}
	s.debugf("loop_once.selected phase=%q session=%q session_status=%q attempts=%d", phaseRun.Name, sess.ID, sess.Status, sess.Attempts)

	s.currentPhase = phaseRun.Name
	r, err := s.runSession(ctx, phaseRun, sess, true)
	s.setLastRuns([]RunRecord{r})
	saveErr := s.saveRuntime()
	s.debugf("loop_once.after_run session=%q result_code=%d err=%q session_status=%q review=%q last_error=%q save_err=%q summary=%s", sess.ID, r.Code, errString(err), sess.Status, sess.ReviewVerdict, sess.LastError, errString(saveErr), s.runtimeSummary())
	if err != nil {
		if saveErr != nil {
			return saveErr
		}
		if sess.Status == sessionStatusFailed || sess.Status == sessionStatusRejected {
			s.StatusLine = fmt.Sprintf("Sesion %s en recuperacion (%s).", sess.ID, sess.Status)
			s.debugf("loop_once.recovery session=%q status=%q status_line=%q", sess.ID, sess.Status, s.StatusLine)
			if hasStop(s.Root) {
				s.debugf("loop_once.recovery.stop_present")
				return fmt.Errorf("se genero loop/stop.md; flujo detenido")
			}
			escalated, canEscalate, _ := plan.EscalateModel(sess.Model)
			if !canEscalate && sess.Attempts >= 3 {
				reason := fmt.Sprintf("sesion %s fallo %d veces con modelo maximo (%s); se requiere intervencion manual", sess.ID, sess.Attempts, sess.Model)
				s.debugf("loop_once.escalation_exhausted session=%q attempts=%d model=%q", sess.ID, sess.Attempts, sess.Model)
				_ = writeStop(s.Root, reason)
				_ = s.saveRuntime()
				return fmt.Errorf("se genero loop/stop.md; flujo detenido")
			}
			if canEscalate {
				s.debugf("loop_once.escalate_model session=%q from=%q to=%q attempts=%d", sess.ID, sess.Model, escalated, sess.Attempts)
				sess.Model = escalated
				if saveErr2 := s.saveRuntime(); saveErr2 != nil {
					return saveErr2
				}
			}
			return nil
		}
		return err
	}
	if saveErr != nil {
		return saveErr
	}
	if hasStop(s.Root) {
		s.debugf("loop_once.completed_with_stop session=%q", sess.ID)
		return fmt.Errorf("se genero loop/stop.md; flujo detenido")
	}
	s.debugf("loop_once.success session=%q status=%q summary=%s", sess.ID, sess.Status, s.runtimeSummary())
	return nil
}

func hasStop(root string) bool {
	_, err := os.Stat(filepath.Join(root, "loop", "stop.md"))
	return err == nil
}

func (s *State) runSession(ctx context.Context, phaseRun *PhaseRuntime, sess *SessionRuntime, stream bool) (RunRecord, error) {
	runID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), sess.ID)
	activeKey := sess.ID
	previousStatus := sess.Status
	recovery := shouldUseErrorPrompt(previousStatus)
	plannerLabel := promptLabel(sess, recovery)
	sess.Status = sessionStatusRunning
	sess.Attempts++
	sess.UpdatedAt = time.Now().Format(time.RFC3339)
	s.debugf("run_session.start run_id=%q session=%q previous_status=%q recovery=%t planner=%q attempts=%d", runID, sess.ID, previousStatus, recovery, plannerLabel, sess.Attempts)
	if stream {
		s.setActiveRun(activeKey, RunRecord{ID: runID, TaskName: sess.ID, Model: sess.Model})
		defer s.clearActiveRun(activeKey)
	}
	if err := os.MkdirAll(sess.SessionDir, 0o755); err != nil {
		return RunRecord{TaskName: sess.ID, Err: err}, err
	}
	if recovery {
		if err := s.writeSessionFeedback(sess, previousStatus); err != nil {
			sess.Status = sessionStatusFailed
			sess.LastError = err.Error()
			s.debugf("run_session.feedback.error session=%q err=%q", sess.ID, err)
			return RunRecord{TaskName: sess.ID, Err: err}, err
		}
	}

	workDir, err := s.ensureWorkspace(sess)
	if err != nil {
		sess.Status = sessionStatusFailed
		sess.LastError = err.Error()
		s.debugf("run_session.ensure_workspace.error session=%q err=%q", sess.ID, err)
		return RunRecord{TaskName: sess.ID, Err: err}, err
	}
	s.debugf("run_session.workspace_ready session=%q workdir=%q branch=%q", sess.ID, workDir, sess.Branch)

	promptPath, err := s.prepareSessionPrompt(sess, recovery)
	if err != nil {
		sess.Status = sessionStatusFailed
		sess.LastError = err.Error()
		s.debugf("run_session.prepare_prompt.error session=%q err=%q", sess.ID, err)
		return RunRecord{TaskName: sess.ID, Err: err}, err
	}

	if stream {
		s.appendActiveStdout(activeKey, fmt.Sprintf("=== %s: generando prompt omega ===\n", plannerLabel))
	}
	alphaStdout, alphaStderr, alphaCode, alphaErr := s.execOmega(ctx, workDir, sess.Model, promptPath, sess.SessionDir, streamWriter(stream, s.appendActiveStdout, activeKey), streamWriter(stream, s.appendActiveStderr, activeKey))
	s.debugf("run_session.planner_complete session=%q planner=%q rc=%d err=%q stderr_nonempty=%t stdout_len=%d", sess.ID, plannerLabel, alphaCode, errString(alphaErr), strings.TrimSpace(alphaStderr) != "", len(alphaStdout))
	if alphaCode != 0 || strings.TrimSpace(alphaStderr) != "" || alphaErr != nil {
		rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: fmt.Sprintf("=== %s ===\n%s", plannerLabel, alphaStdout), Stderr: fmt.Sprintf("=== %s ===\n%s", plannerLabel, alphaStderr), Code: alphaCode, Model: sess.Model, Err: alphaErr}
		sess.Status = sessionStatusFailed
		sess.LastError = firstNonEmpty(strings.TrimSpace(alphaStderr), errString(alphaErr), fmt.Sprintf("returncode %d", alphaCode))
		_ = s.persistRun(rr)
		return rr, fmt.Errorf("%s fallo para sesion %s", strings.ToLower(plannerLabel), sess.ID)
	}

	omegaPrompt := strings.TrimSpace(alphaStdout)
	if omegaPrompt == "" {
		err := fmt.Errorf("%s no genero prompt omega", strings.ToLower(plannerLabel))
		sess.Status = sessionStatusFailed
		sess.LastError = err.Error()
		rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: fmt.Sprintf("=== %s ===\n%s", plannerLabel, alphaStdout), Stderr: err.Error(), Code: 1, Model: sess.Model, Err: err}
		_ = s.persistRun(rr)
		return rr, err
	}
	omegaPromptPath := filepath.Join(sess.SessionDir, "omega.md")
	if err := os.WriteFile(omegaPromptPath, []byte(omegaPrompt+"\n"), 0o644); err != nil {
		return RunRecord{TaskName: sess.ID, Err: err}, err
	}

	if stream {
		s.appendActiveStdout(activeKey, "\n=== OMEGA: ejecutando sesion ===\n")
	}
	omegaStdout, omegaStderr, omegaCode, omegaErr := s.execOmega(ctx, workDir, sess.Model, omegaPromptPath, sess.SessionDir, streamWriter(stream, s.appendActiveStdout, activeKey), streamWriter(stream, s.appendActiveStderr, activeKey))
	s.debugf("run_session.omega_complete session=%q rc=%d err=%q stderr_nonempty=%t completion_marker=%t stdout_len=%d", sess.ID, omegaCode, errString(omegaErr), strings.TrimSpace(omegaStderr) != "", strings.Contains(omegaStdout, omegaCompletionMarker), len(omegaStdout))

	gitStdout, gitStderr := "", ""
	if omegaCode == 0 && strings.TrimSpace(omegaStderr) == "" && omegaErr == nil {
		gitStdout, gitStderr, _ = s.captureSessionCommit(sess)
		s.debugf("run_session.capture_commit session=%q git_stdout_len=%d git_stderr_nonempty=%t", sess.ID, len(gitStdout), strings.TrimSpace(gitStderr) != "")
	}

	omegaCompletionMissing := omegaCode == 0 && strings.TrimSpace(omegaStderr) == "" && omegaErr == nil && !strings.Contains(omegaStdout, omegaCompletionMarker)
	reviewStdout, reviewStderr, reviewCode, reviewErr := s.runReview(ctx, workDir, phaseRun, sess, stream, activeKey, omegaCompletionMissing)
	sess.Status = sessionStatusReviewPending
	s.debugf("run_session.review_complete session=%q rc=%d err=%q stderr_nonempty=%t approved=%t omega_completion_missing=%t", sess.ID, reviewCode, errString(reviewErr), strings.TrimSpace(reviewStderr) != "", reviewApproved(reviewStdout), omegaCompletionMissing)

	stdout := strings.Join([]string{
		fmt.Sprintf("=== %s ===\n%s", plannerLabel, alphaStdout),
		"=== OMEGA ===\n" + omegaStdout,
		"=== GIT ===\n" + gitStdout,
		"=== REVIEW ===\n" + reviewStdout,
	}, "\n\n")
	stderr := strings.Join([]string{
		fmt.Sprintf("=== %s ===\n%s", plannerLabel, alphaStderr),
		"=== OMEGA ===\n" + omegaStderr,
		"=== GIT ===\n" + gitStderr,
		"=== REVIEW ===\n" + reviewStderr,
	}, "\n\n")

	if omegaCode != 0 || strings.TrimSpace(omegaStderr) != "" || omegaErr != nil {
		sess.Status = sessionStatusFailed
		sess.LastError = firstNonEmpty(strings.TrimSpace(omegaStderr), errString(omegaErr), "omega no confirmo finalizacion")
		s.debugf("run_session.fail_omega session=%q last_error=%q", sess.ID, sess.LastError)
		rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: stdout, Stderr: stderr, Code: omegaCode, Model: sess.Model, Err: omegaErr}
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		_ = s.persistRun(rr)
		return rr, fmt.Errorf("sesion %s fallo", sess.ID)
	}
	if reviewCode != 0 || reviewErr != nil || strings.TrimSpace(reviewStderr) != "" {
		sess.Status = sessionStatusFailed
		sess.LastError = firstNonEmpty(strings.TrimSpace(reviewStderr), errString(reviewErr), "review fallo")
		s.debugf("run_session.fail_review session=%q last_error=%q", sess.ID, sess.LastError)
		rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: stdout, Stderr: stderr, Code: reviewCode, Model: sess.Model, Err: reviewErr}
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		_ = s.persistRun(rr)
		return rr, fmt.Errorf("review fallo para %s", sess.ID)
	}
	if !reviewApproved(reviewStdout) {
		if omegaCompletionMissing {
			sess.Status = sessionStatusFailed
		} else {
			sess.Status = sessionStatusRejected
		}
		sess.ReviewVerdict = "rejected"
		sess.LastError = firstNonEmpty(reviewFailureReason(omegaCompletionMissing, reviewStdout), "review rechazo la sesion")
		s.debugf("run_session.review_rejected session=%q status=%q last_error=%q", sess.ID, sess.Status, sess.LastError)
		rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: stdout, Stderr: stderr, Code: 0, Model: sess.Model}
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		_ = s.persistRun(rr)
		return rr, fmt.Errorf("review rechazo %s", sess.ID)
	}

	sess.ReviewVerdict = "approved"
	sess.Status = sessionStatusApproved
	if omegaCompletionMissing {
		sess.LastError = ""
	}
	s.debugf("run_session.review_approved session=%q merge_pending=true", sess.ID)
	mergeStdout, err := s.mergeApprovedSession(sess, stream, activeKey)
	stdout = strings.Join([]string{
		stdout,
		"=== MERGE ===\n" + mergeStdout,
	}, "\n\n")
	rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: stdout, Stderr: stderr, Code: 0, Model: sess.Model}
	if err != nil {
		sess.Status = sessionStatusFailed
		sess.LastError = err.Error()
		s.debugf("run_session.merge_failed session=%q err=%q", sess.ID, err)
		rr.Err = err
		rr.Code = 1
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		_ = s.persistRun(rr)
		return rr, err
	}
	sess.Status = sessionStatusMerged
	sess.LastError = ""
	s.debugf("run_session.merged session=%q squash_commit=%q phase=%q", sess.ID, sess.SquashCommit, phaseRun.Name)
	if stream {
		s.setActiveRunResult(activeKey, rr)
	}
	_ = s.persistRun(rr)
	if sess.Kind == "upgrade" {
		if err := s.reloadState(); err != nil {
			return rr, err
		}
		if !plan.VersionMismatch(s.Plan) {
			s.Runtime.UpgradeNeeded = false
			phaseRun.Status = phaseStatusCompleted
		}
	} else if allSessionsMerged(phaseRun) {
		phaseRun.Status = phaseStatusCompleted
	}
	return rr, nil
}

func streamWriter(enabled bool, fn func(string, string), key string) func(string) {
	if !enabled {
		return nil
	}
	return func(chunk string) { fn(key, chunk) }
}

func shouldUseErrorPrompt(status string) bool {
	return status == sessionStatusFailed || status == sessionStatusRejected
}

func promptLabel(sess *SessionRuntime, recovery bool) string {
	if sess != nil && sess.Kind == "upgrade" {
		return "UPGRADE"
	}
	if recovery {
		return "ERROR"
	}
	return "ALPHA"
}

func (s *State) writeSessionFeedback(sess *SessionRuntime, previousStatus string) error {
	feedback := strings.TrimSpace(strings.Join([]string{
		"# Feedback de recuperacion",
		fmt.Sprintf("- intento_anterior: %d", maxInt(sess.Attempts-1, 0)),
		fmt.Sprintf("- estado_anterior: %s", valueOrDash(previousStatus)),
		fmt.Sprintf("- review_anterior: %s", valueOrDash(sess.ReviewVerdict)),
		fmt.Sprintf("- ultimo_error: %s", valueOrDash(sess.LastError)),
	}, "\n"))
	return os.WriteFile(filepath.Join(sess.SessionDir, "feedback.md"), []byte(feedback+"\n"), 0o644)
}

func (s *State) prepareSessionPrompt(sess *SessionRuntime, recovery bool) (string, error) {
	if err := os.MkdirAll(sess.SessionDir, 0o755); err != nil {
		return "", err
	}
	kind := "alpha"
	if sess.Kind == "upgrade" {
		kind = "upgrade"
	} else if recovery {
		kind = "error"
	}
	tpl, err := config.ReadPrompt(kind)
	if err != nil {
		return "", err
	}
	ctx := s.buildSessionContext(sess)
	content := strings.NewReplacer(
		"{{task_name}}", sess.Goal,
		"{{task_description}}", sess.Description,
		"{{task_model}}", sess.Model,
		"{{context}}", ctx,
		"{{plan_version}}", valueOrDash(strings.TrimSpace(s.Plan.Version)),
		"{{target_version}}", forgeworld.CurrentPlanVersion,
		"{{feedback_file}}", filepath.ToSlash(filepath.Join(sess.SessionDir, "feedback.md")),
	).Replace(tpl)
	promptPath := filepath.Join(sess.SessionDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	ctxData := map[string]string{"goal": sess.Goal, "phase": sess.PhaseName, "model": sess.Model, "context": ctx}
	ctxB, _ := yaml.Marshal(ctxData)
	if err := os.WriteFile(filepath.Join(sess.SessionDir, "context.yml"), ctxB, 0o644); err != nil {
		return "", err
	}
	return promptPath, nil
}

func (s *State) buildSessionContext(sess *SessionRuntime) string {
	parts := []string{}
	if strings.TrimSpace(s.Plan.Context) != "" {
		parts = append(parts, s.Plan.Context)
	}
	if sess.Kind == "upgrade" {
		parts = append(parts, "Actualizar exclusivamente plan/plan.yml a la nueva version de metodologia.")
		return strings.Join(parts, "\n\n")
	}
	for _, phase := range s.Plan.Phases {
		if phase.Name != sess.PhaseName {
			continue
		}
		if strings.TrimSpace(phase.Context) != "" {
			parts = append(parts, phase.Context)
		}
		for _, node := range phase.Tasks {
			if node.Task != nil && node.Task.Name == sess.TaskName && strings.TrimSpace(node.Task.Context) != "" {
				parts = append(parts, node.Task.Context)
			}
		}
		break
	}
	return strings.Join(parts, "\n\n")
}

func (s *State) runReview(ctx context.Context, workDir string, phase *PhaseRuntime, sess *SessionRuntime, stream bool, activeKey string, omegaCompletionMissing bool) (string, string, int, error) {
	tpl, err := config.ReadPrompt("review")
	if err != nil {
		return "", err.Error(), 1, err
	}
	diffSummary, err := s.diffSummary(sess)
	if err != nil {
		diffSummary = "sin diff disponible: " + err.Error()
	}
	if omegaCompletionMissing {
		diffSummary = strings.TrimSpace(diffSummary) + "\n\nIncidencia de protocolo detectada: Omega termino sin emitir FORGEWORLD_TASK_COMPLETE. Decide si los cambios ya cumplen el objetivo y se puede cerrar la tarea, o si la sesion debe continuar."
	}
	content := strings.NewReplacer(
		"{{session_id}}", sess.ID,
		"{{session_goal}}", sess.Goal,
		"{{phase_name}}", phase.Name,
		"{{diff_summary}}", diffSummary,
	).Replace(tpl)
	promptPath := filepath.Join(sess.SessionDir, "review.md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", err.Error(), 1, err
	}
	if stream {
		s.appendActiveStdout(activeKey, "\n=== REVIEW: evaluando sesion ===\n")
	}
	return s.execOmega(ctx, workDir, sess.Model, promptPath, sess.SessionDir, streamWriter(stream, s.appendActiveStdout, activeKey), streamWriter(stream, s.appendActiveStderr, activeKey))
}

func reviewApproved(out string) bool {
	first := strings.ToUpper(strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0]))
	first = strings.TrimLeft(first, "*_ \t")
	return strings.HasPrefix(first, "APPROVED")
}

func reviewFailureReason(omegaCompletionMissing bool, reviewStdout string) string {
	if !omegaCompletionMissing {
		return strings.TrimSpace(reviewStdout)
	}
	reviewSummary := strings.TrimSpace(reviewStdout)
	if reviewSummary == "" {
		return "omega no confirmo finalizacion; review indico continuar la sesion"
	}
	return "omega no confirmo finalizacion; review indico continuar la sesion: " + reviewSummary
}

func allSessionsMerged(phase *PhaseRuntime) bool {
	if len(phase.Sessions) == 0 {
		return true
	}
	for _, sess := range phase.Sessions {
		if sess.Status != sessionStatusMerged {
			return false
		}
	}
	return true
}

func (s *State) captureSessionCommit(sess *SessionRuntime) (string, string, error) {
	if !s.Runtime.GitEnabled || sess.WorktreePath == "" {
		return "repositorio sin git o sin worktree dedicado", "", nil
	}
	status, err := s.gitOutput(sess.WorktreePath, "status", "--porcelain")
	if err != nil {
		return "", err.Error(), err
	}
	if strings.TrimSpace(status) == "" {
		return "sin cambios locales pendientes; se reutilizan commits existentes", "", nil
	}
	if _, err := s.gitOutput(sess.WorktreePath, "add", "-A"); err != nil {
		return "", err.Error(), err
	}
	if _, err := s.gitOutput(sess.WorktreePath, "commit", "-m", fmt.Sprintf("forgeworld(session): %s", sess.Goal)); err != nil {
		return "", err.Error(), err
	}
	logOut, err := s.gitOutput(sess.WorktreePath, "log", "--oneline", "-1")
	return logOut, "", err
}

func (s *State) diffSummary(sess *SessionRuntime) (string, error) {
	if !s.Runtime.GitEnabled || sess.Branch == "" || sess.BaseBranch == "" {
		return "sesion sin diff git; revisar cambios por logs", nil
	}
	stat, err := s.gitOutput(s.Root, "diff", "--stat", fmt.Sprintf("%s...%s", sess.BaseBranch, sess.Branch))
	if err != nil {
		return "", err
	}
	names, err := s.gitOutput(s.Root, "diff", "--name-status", fmt.Sprintf("%s...%s", sess.BaseBranch, sess.Branch))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stat + "\n\n" + names), nil
}

func (s *State) mergeApprovedSession(sess *SessionRuntime, stream bool, activeKey string) (string, error) {
	if !s.Runtime.GitEnabled || sess.Branch == "" {
		return "repositorio sin git o sin branch de sesion; no aplica merge", nil
	}
	if stream {
		s.appendActiveStdout(activeKey, "\n=== MERGE: squash merge de sesion aprobada ===\n")
	}
	if _, err := s.gitOutput(s.Root, "merge", "--squash", "--no-commit", sess.Branch); err != nil {
		_, _ = s.gitOutput(s.Root, "merge", "--abort")
		return "", fmt.Errorf("squash merge fallo para %s: %w", sess.ID, err)
	}
	commitMsg := fmt.Sprintf("forgeworld(merge): %s", sess.Goal)
	if _, err := s.gitOutput(s.Root, "commit", "-m", commitMsg); err != nil {
		_, _ = s.gitOutput(s.Root, "merge", "--abort")
		return "", fmt.Errorf("commit de squash merge fallo para %s: %w", sess.ID, err)
	}
	parts := []string{"squash merge aplicado", commitMsg}
	head, err := s.gitOutput(s.Root, "rev-parse", "HEAD")
	if err == nil {
		sess.SquashCommit = strings.TrimSpace(head)
		parts = append(parts, "commit "+sess.SquashCommit)
	}
	if err := s.cleanupWorktree(sess); err != nil {
		return strings.Join(parts, "\n"), err
	}
	parts = append(parts, "worktree limpiado")
	return strings.Join(parts, "\n"), nil
}

func (s *State) ensureWorkspace(sess *SessionRuntime) (string, error) {
	if !s.Runtime.GitEnabled {
		return s.Root, nil
	}
	if sess.WorktreePath != "" {
		if _, err := os.Stat(sess.WorktreePath); err == nil {
			return sess.WorktreePath, nil
		}
		// Worktree directory is gone; clear workspace info and recreate below.
		s.debugf("ensure_workspace.worktree_missing session=%q path=%q resetting", sess.ID, sess.WorktreePath)
		sess.WorktreePath = ""
		sess.Branch = ""
		sess.BaseBranch = ""
	}

	baseBranch, err := s.gitOutput(s.Root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	baseBranch = strings.TrimSpace(baseBranch)
	s.Runtime.BaseBranch = baseBranch
	worktreePath := filepath.Join(s.Root, "loop", "worktrees", sess.ID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", err
	}
	branch := fmt.Sprintf("forgeworld/%s", sess.ID)
	if _, err := s.gitOutput(s.Root, "worktree", "add", worktreePath, "-b", branch, baseBranch); err != nil {
		// Branch may already exist without a worktree; reuse it.
		if _, err2 := s.gitOutput(s.Root, "worktree", "add", worktreePath, branch); err2 != nil {
			return "", err
		}
	}
	sess.Branch = branch
	sess.BaseBranch = baseBranch
	sess.WorktreePath = worktreePath
	return worktreePath, nil
}

func (s *State) cleanupWorktree(sess *SessionRuntime) error {
	if !s.Runtime.GitEnabled || sess.WorktreePath == "" {
		return nil
	}
	if _, err := s.gitOutput(s.Root, "worktree", "remove", "--force", sess.WorktreePath); err != nil {
		return err
	}
	if sess.Branch != "" {
		if _, err := s.gitOutput(s.Root, "branch", "-D", sess.Branch); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return outb.String(), nil
}

func (s *State) detectGit() {
	if _, err := s.gitOutput(s.Root, "rev-parse", "--show-toplevel"); err == nil {
		s.Runtime.GitEnabled = true
	}
}

func (s *State) Fix(ctx context.Context, onStdout func(string), onStderr func(string)) (RunRecord, error) {
	if err := s.reloadState(); err != nil {
		return RunRecord{TaskName: "ordenanamiento", Err: err}, err
	}
	taskDir := filepath.Join(s.Root, "loop", "tasks", "ordenanamiento")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return RunRecord{TaskName: "ordenanamiento", Err: err}, err
	}
	runID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), "ordenanamiento")
	stdout, stderr, code, runErr := s.runOrdenanamiento(ctx, s.Root, plan.ModelMedium, taskDir, onStdout, onStderr)
	rr := RunRecord{
		ID:       runID,
		TaskName: "ordenanamiento",
		Stdout:   "=== ORDENANAMIENTO ===\n" + stdout,
		Stderr:   "=== ORDENANAMIENTO ===\n" + stderr,
		Code:     code,
		Model:    plan.ModelMedium,
		Err:      runErr,
	}
	_ = s.persistRun(rr)
	if err := s.reloadState(); err != nil {
		return rr, err
	}
	if code != 0 || strings.TrimSpace(stderr) != "" || runErr != nil {
		return rr, fmt.Errorf("ordenanamiento fallo")
	}
	return rr, nil
}

func (s *State) execOmega(
	ctx context.Context,
	workDir, modelTier, promptPath, taskDir string,
	onStdout func(string),
	onStderr func(string),
) (string, string, int, error) {
	mappedModel := s.Config.Models[modelTier]
	args := make([]string, 0, len(s.Config.Executor.Args))
	for _, arg := range s.Config.Executor.Args {
		r := strings.NewReplacer(
			"{{model}}", mappedModel,
			"{{model_tier}}", modelTier,
			"{{prompt}}", promptPath,
			"{{task_dir}}", taskDir,
		)
		args = append(args, r.Replace(arg))
	}
	cmd := exec.CommandContext(ctx, s.Config.Executor.Command, args...)
	cmd.Dir = workDir
	var outb, errb bytes.Buffer
	cmd.Stdout = io.MultiWriter(&outb, chunkWriter{fn: onStdout})
	cmd.Stderr = io.MultiWriter(&errb, chunkWriter{fn: onStderr})
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	return outb.String(), errb.String(), code, err
}

func formatValidationErrors(errs []error) string {
	if len(errs) == 0 {
		return "plan/plan.yml valido"
	}
	lines := make([]string, 0, len(errs))
	for _, err := range errs {
		lines = append(lines, "- "+err.Error())
	}
	return strings.Join(lines, "\n")
}

func (s *State) runOrdenanamiento(
	ctx context.Context,
	workDir, modelTier, taskDir string,
	onStdout func(string),
	onStderr func(string),
) (string, string, int, error) {
	emitStdout := func(msg string) {
		if onStdout != nil && msg != "" {
			onStdout(msg)
		}
	}
	p, _, err := plan.Load(workDir)
	if err != nil {
		return "", err.Error(), 1, err
	}
	emitStdout("Validando plan/plan.yml...\n")
	errs := plan.Validate(p)
	blocking := plan.BlockingValidationErrors(errs)
	if len(blocking) == 0 {
		if plan.VersionMismatch(p) {
			emitStdout("plan/plan.yml no tiene errores bloqueantes; actualizando version del plan...\n")
			if err := ensureCurrentPlanVersion(workDir, p); err != nil {
				return "", err.Error(), 1, err
			}
			emitStdout("Version del plan actualizada.\n")
			return "plan/plan.yml valido", "", 0, nil
		}
		emitStdout("plan/plan.yml ya es valido; no hace falta ordenanamiento.\n")
		return "plan/plan.yml valido", "", 0, nil
	}
	emitStdout("Se detectaron errores de validacion; ejecutando ordenanamiento...\n")
	tpl, err := config.ReadPrompt("ordenanamiento")
	if err != nil {
		return "", err.Error(), 1, err
	}
	content := strings.NewReplacer("{{validation_errors}}", formatValidationErrors(errs)).Replace(tpl)
	promptPath := filepath.Join(taskDir, "ordenanamiento.md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", err.Error(), 1, err
	}
	emitStdout(fmt.Sprintf("Prompt de ordenanamiento generado en %s.\n", promptPath))
	emitStdout("Lanzando executor...\n")
	out, errOut, code, runErr := s.execOmega(ctx, workDir, modelTier, promptPath, taskDir, onStdout, onStderr)
	if code != 0 || strings.TrimSpace(errOut) != "" || runErr != nil {
		return out, errOut, code, runErr
	}
	emitStdout("Executor finalizado; revalidando plan/plan.yml...\n")
	after, _, err := plan.Load(workDir)
	if err != nil {
		return out, err.Error(), 1, err
	}
	if plan.VersionMismatch(after) {
		emitStdout("Actualizando version del plan a la metodologia actual...\n")
		if err := ensureCurrentPlanVersion(workDir, after); err != nil {
			return out, err.Error(), 1, err
		}
		after, _, err = plan.Load(workDir)
		if err != nil {
			return out, err.Error(), 1, err
		}
	}
	afterErrs := plan.Validate(after)
	afterBlocking := plan.BlockingValidationErrors(afterErrs)
	if len(afterBlocking) > 0 {
		msg := "plan sigue invalido tras ordenanamiento:\n" + formatValidationErrors(afterBlocking)
		return out, msg, 1, fmt.Errorf("plan invalido tras ordenanamiento")
	}
	emitStdout("Revalidacion completada: plan/plan.yml valido.\n")
	return out, errOut, 0, nil
}

func ensureCurrentPlanVersion(workDir string, p *plan.Plan) error {
	if strings.TrimSpace(p.Version) == strings.TrimSpace(forgeworld.CurrentPlanVersion) {
		return nil
	}
	p.Version = forgeworld.CurrentPlanVersion
	return plan.Save(filepath.Join(workDir, "plan", "plan.yml"), p)
}

func (s *State) persistRun(r RunRecord) error {
	dir := filepath.Join(s.Root, "loop", "runs", r.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(r.Stdout), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(r.Stderr), 0o644); err != nil {
		return err
	}
	meta := map[string]interface{}{"task": r.TaskName, "model": r.Model, "returncode": r.Code, "error": errString(r.Err)}
	b, _ := yaml.Marshal(meta)
	return os.WriteFile(filepath.Join(dir, "meta.yml"), b, 0o644)
}

func writeStop(root, reason string) error {
	path := filepath.Join(root, "loop", "stop.md")
	body := fmt.Sprintf("# Ejecucion detenida\n\nMotivo: %s\n", reason)
	return os.WriteFile(path, []byte(body), 0o644)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type chunkWriter struct {
	fn func(string)
}

func (w chunkWriter) Write(p []byte) (int, error) {
	if w.fn != nil && len(p) > 0 {
		w.fn(string(p))
	}
	return len(p), nil
}

func (s *State) reloadState() error {
	p, path, err := plan.Load(s.Root)
	if err != nil {
		s.debugf("reload_state.plan_load.error err=%q", err)
		return err
	}
	changedPhase0 := plan.EnsurePhase0(p)
	changedCompletion := plan.ReconcileCompletion(p)
	if changedPhase0 || changedCompletion {
		if err := plan.Save(path, p); err != nil {
			s.debugf("reload_state.plan_save.error err=%q", err)
			return err
		}
	}
	s.Plan = p
	s.PlanPath = path
	rt, rtPath, err := loadRuntime(s.Root, p)
	if err != nil {
		s.debugf("reload_state.runtime_load.error err=%q", err)
		return err
	}
	s.Runtime = rt
	s.RuntimePath = rtPath
	s.detectGit()
	s.debugf("reload_state.complete changed_phase0=%t changed_completion=%t runtime_path=%q summary=%s", changedPhase0, changedCompletion, s.RuntimePath, s.runtimeSummary())
	return s.saveRuntime()
}

func (s *State) saveRuntime() error {
	if s.Runtime == nil {
		return nil
	}
	err := saveRuntime(s.RuntimePath, s.Runtime)
	if err != nil {
		s.debugf("save_runtime.error path=%q err=%q", s.RuntimePath, err)
		return err
	}
	s.debugf("save_runtime.ok path=%q summary=%s", s.RuntimePath, s.runtimeSummary())
	return nil
}

func (s *State) setLastRuns(runs []RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastRuns = append([]RunRecord(nil), runs...)
}

func (s *State) SnapshotLastRuns() []RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]RunRecord(nil), s.LastRuns...)
}

func (s *State) setActiveRun(key string, run RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRuns == nil {
		s.activeRuns = make(map[string]*RunRecord)
	}
	if _, exists := s.activeRuns[key]; !exists {
		s.activeOrder = append(s.activeOrder, key)
	}
	rc := run
	s.activeRuns[key] = &rc
}

func (s *State) setActiveRunResult(key string, run RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeRuns[key]
	if active == nil {
		rc := run
		s.activeRuns[key] = &rc
		return
	}
	active.ID = run.ID
	active.TaskName = run.TaskName
	active.Model = run.Model
	active.Code = run.Code
	active.Err = run.Err
}

func (s *State) clearActiveRun(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeRuns, key)
	filtered := s.activeOrder[:0]
	for _, k := range s.activeOrder {
		if k != key {
			filtered = append(filtered, k)
		}
	}
	s.activeOrder = filtered
}

func (s *State) appendActiveStdout(key, chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeRuns[key]
	if active == nil {
		return
	}
	active.Stdout += chunk
}

func (s *State) appendActiveStderr(key, chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.activeRuns[key]
	if active == nil {
		return
	}
	active.Stderr += chunk
}

func (s *State) SnapshotActiveRuns() []RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.activeRuns) == 0 {
		return nil
	}
	out := make([]RunRecord, 0, len(s.activeRuns))
	for _, k := range s.activeOrder {
		if r := s.activeRuns[k]; r != nil {
			out = append(out, *r)
		}
	}
	return out
}

func (s *State) debugf(format string, args ...interface{}) {
	path := filepath.Join(s.Root, "loop", "runtime", "diagnostic.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
	_, _ = f.WriteString(line)
}

func (s *State) runtimeSummary() string {
	if s == nil || s.Runtime == nil {
		return "runtime=nil"
	}
	parts := make([]string, 0, len(s.Runtime.Phases))
	for _, phase := range s.Runtime.Phases {
		if phase == nil {
			continue
		}
		sessionParts := make([]string, 0, len(phase.Sessions))
		for _, sess := range phase.Sessions {
			if sess == nil {
				continue
			}
			sessionParts = append(sessionParts, fmt.Sprintf("%s:%s:%d:%s", sess.ID, sess.Status, sess.Attempts, sess.ReviewVerdict))
		}
		parts = append(parts, fmt.Sprintf("%s[%s]{%s}", phase.ID, phase.Status, strings.Join(sessionParts, ",")))
	}
	return strings.Join(parts, " | ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return strings.TrimSpace(v)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

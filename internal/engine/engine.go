package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

const nextRoleSignalPrefix = "FORGEWORLD_NEXT:"

// maxRoleChainIterations is the safety-net cap on role-chain steps per session.
// It is a var (not const) so tests can lower it to exercise the exhaustion path quickly.
var maxRoleChainIterations = 20

type planWorkResult struct {
	out string
	err error
}

type planWork struct {
	ctx      context.Context
	fn       func(ctx context.Context) (string, error)
	resultCh chan<- planWorkResult
}

type State struct {
	Root        string
	Config      *config.Config
	Tasks       []*plan.Task
	RuntimePath string
	Runtime     *RuntimeState
	StatusLine  string

	mu          sync.RWMutex
	LastRuns    []RunRecord
	activeRuns  map[string]*RunRecord
	activeOrder []string
	planWorkCh  chan planWork
}

func LoadState(root string) (*State, error) {
	cfg, err := config.LoadLocal(root)
	if err != nil {
		return nil, err
	}
	tasks, err := plan.LoadTasks(root)
	if err != nil {
		return nil, err
	}
	rt, rtPath, err := loadRuntime(root, tasks)
	if err != nil {
		return nil, err
	}
	st := &State{Root: root, Config: cfg, Tasks: tasks, RuntimePath: rtPath, Runtime: rt}
	st.planWorkCh = make(chan planWork, 1)
	st.detectGit()
	st.startPlanWorker(context.Background())
	return st, nil
}

func (s *State) startPlanWorker(ctx context.Context) {
	go func() {
		for {
			select {
			case work, ok := <-s.planWorkCh:
				if !ok {
					return
				}
				out, err := work.fn(work.ctx)
				work.resultCh <- planWorkResult{out: out, err: err}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *State) Tree(selectedTask string) string {
	var b strings.Builder
	for _, sess := range s.Runtime.Sessions {
		tm := sessionMarker(sess.Status, sess.Role)
		if strings.TrimSpace(selectedTask) != "" && sess.ID == selectedTask {
			tm = "[*]"
		}
		fmt.Fprintf(&b, "%s %s (%s)\n", tm, sess.Goal, sess.Model)
	}
	return b.String()
}

func sessionMarker(status, role string) string {
	switch status {
	case sessionStatusMerged:
		return "[x]"
	case sessionStatusRunning, sessionStatusReviewPending, sessionStatusApproved:
		if role != "" {
			return fmt.Sprintf("[>:%s]", role)
		}
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

	sess := nextRunnableSessionFlat(s.Runtime)
	if sess == nil {
		s.StatusLine = "Plan completado."
		s.debugf("loop_once.no_runnable_session summary=%s", s.runtimeSummary())
		if err := s.saveRuntime(); err != nil {
			s.debugf("loop_once.save_runtime.error err=%q", err)
			return err
		}
		return nil
	}
	s.debugf("loop_once.selected session=%q session_status=%q attempts=%d", sess.ID, sess.Status, sess.Attempts)

	r, err := s.runSession(ctx, sess, true)
	s.setLastRuns([]RunRecord{r})
	saveErr := s.saveRuntime()
	s.debugf("loop_once.after_run session=%q result_code=%d err=%q session_status=%q last_error=%q save_err=%q summary=%s", sess.ID, r.Code, errString(err), sess.Status, sess.LastError, errString(saveErr), s.runtimeSummary())
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

func (s *State) runSession(ctx context.Context, sess *SessionRuntime, stream bool) (RunRecord, error) {
	runID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), sess.ID)
	activeKey := sess.ID
	previousStatus := sess.Status
	recovery := shouldUseErrorPrompt(previousStatus)
	plannerLabel := promptLabel(recovery)
	plannerKind := "alpha"
	if recovery {
		plannerKind = "error"
	}
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

	// Log alpha/error phase as step 0 in roles directory
	s.saveRoleLog(sess, 0, plannerKind, alphaStdout, alphaStderr)

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

	if stream {
		s.appendActiveStdout(activeKey, "\n=== ROLE CHAIN ===\n")
	}

	chainErr := s.executeRoleChain(ctx, sess, omegaPrompt, workDir, stream, activeKey)

	stdout := fmt.Sprintf("=== %s ===\n%s\n\n=== ROLE CHAIN ===\n[ver %s/roles/]", plannerLabel, alphaStdout, sess.SessionDir)
	stderr := fmt.Sprintf("=== %s ===\n%s", plannerLabel, alphaStderr)
	rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: stdout, Stderr: stderr, Code: 0, Model: sess.Model}

	if chainErr != nil {
		rr.Code = 1
		rr.Err = chainErr
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		_ = s.persistRun(rr)
		return rr, chainErr
	}

	// Safety net: ensure task is marked complete if session is merged
	if sess.Status == sessionStatusMerged {
		if task := s.findTask(sess.TaskName); task != nil {
			_ = plan.SaveTaskComplete(s.Root, task)
		}
	}

	if stream {
		s.setActiveRunResult(activeKey, rr)
	}
	_ = s.persistRun(rr)
	return rr, nil
}

// parseNextRole parses the last line of stdout for a FORGEWORLD_NEXT: <role> signal.
// Returns ("", false) if no signal found. Returns ("judge", true) if role is unknown.
func parseNextRole(stdout string, knownRoles map[string]bool) (role string, found bool) {
	trimmed := strings.TrimRight(stdout, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 {
		return "", false
	}
	lastLine := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(lastLine, nextRoleSignalPrefix) {
		return "", false
	}
	role = strings.TrimSpace(strings.TrimPrefix(lastLine, nextRoleSignalPrefix))
	if role == "" || !knownRoles[role] {
		return "judge", true
	}
	return role, true
}

// isProjectLocalRole returns true if the role is defined in loop/roles/.
func (s *State) isProjectLocalRole(role string) bool {
	_, err := os.Stat(filepath.Join(s.Root, "loop", "roles", role+".md"))
	return err == nil
}

// readRolePrompt loads a role prompt. Falls back to "judge" if role not found.
func (s *State) readRolePrompt(role string) (string, error) {
	content, _, err := config.ReadRolePrompt(s.Root, role)
	if err != nil {
		if errors.Is(err, config.ErrRoleNotFound) {
			content, _, err = config.ReadRolePrompt(s.Root, "judge")
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}
	return content, nil
}

// buildRoleVars builds the full variable map for role prompt rendering.
func (s *State) buildRoleVars(sess *SessionRuntime, extras map[string]string) map[string]string {
	ctx := s.buildSessionContext(sess)
	availableRoles := strings.Join(config.ListAvailableRoles(s.Root), ", ")
	vars := map[string]string{
		"{{task_name}}":        sess.Goal,
		"{{task_description}}": sess.Description,
		"{{task_model}}":       sess.Model,
		"{{context}}":          ctx,
		"{{session_id}}":       sess.ID,
		"{{session_dir}}":      sess.SessionDir,
		"{{feedback_file}}":    filepath.Join(sess.SessionDir, "feedback.md"),
		"{{available_roles}}":  availableRoles,
		"{{previous_role}}":    sess.Role,
		"{{diff_summary}}":     "",
		"{{merge_result}}":     "",
	}
	for k, v := range extras {
		vars[k] = v
	}
	return vars
}

// prepareRolePrompt renders a role prompt template and writes it to the session dir.
func (s *State) prepareRolePrompt(sess *SessionRuntime, role string, extras map[string]string) (string, error) {
	tplContent, err := s.readRolePrompt(role)
	if err != nil {
		return "", err
	}
	vars := s.buildRoleVars(sess, extras)
	pairs := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		pairs = append(pairs, k, v)
	}
	content := strings.NewReplacer(pairs...).Replace(tplContent)
	promptPath := filepath.Join(sess.SessionDir, role+".md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return promptPath, nil
}

// saveRoleLog writes role execution output to loop/sessions/<id>/roles/<NNN>-<role>.log.
func (s *State) saveRoleLog(sess *SessionRuntime, step int, role, stdout, stderr string) {
	logDir := filepath.Join(sess.SessionDir, "roles")
	_ = os.MkdirAll(logDir, 0o755)
	name := fmt.Sprintf("%03d-%s.log", step, role)
	body := stdout
	if strings.TrimSpace(stderr) != "" {
		body += "\n--- STDERR ---\n" + stderr
	}
	_ = os.WriteFile(filepath.Join(logDir, name), []byte(body), 0o644)
}

// executeRoleChain runs the omega→judge→merge→done role pipeline.
// omegaPrompt is the content generated by the alpha/error phase.
// workDir is the worktree path (or root if git is disabled).
func (s *State) executeRoleChain(ctx context.Context, sess *SessionRuntime, omegaPrompt, workDir string, stream bool, activeKey string) error {
	rolesDir := filepath.Join(sess.SessionDir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		return err
	}

	omegaPromptPath := filepath.Join(sess.SessionDir, "omega.md")
	if err := os.WriteFile(omegaPromptPath, []byte(omegaPrompt+"\n"), 0o644); err != nil {
		return err
	}

	currentRole := "omega"
	currentPromptPath := omegaPromptPath
	// step starts at 1; step 0 is the alpha/error phase logged in runSession
	step := 1

	knownRolesSlice := config.ListAvailableRoles(s.Root)
	knownRoles := make(map[string]bool, len(knownRolesSlice)+2)
	for _, r := range knownRolesSlice {
		knownRoles[r] = true
	}
	// Always know the core roles even if not listed (e.g. before init)
	for _, r := range []string{"omega", "judge", "merge", "done", "plan", "crit-error", "error"} {
		knownRoles[r] = true
	}

	currentWorkDir := workDir
	var mergeResult string

	for i := 0; i < maxRoleChainIterations; i++ {
		// a. Checkpoint: check for stop at start of every iteration
		if hasStop(s.Root) {
			if sess.LastError == "" {
				sess.LastError = "stop.md detectado"
			}
			sess.Status = sessionStatusFailed
			return fmt.Errorf("stop.md detectado durante role chain de %s", sess.ID)
		}

		// b-c. Update role history
		sess.RoleHistory = append(sess.RoleHistory, currentRole)
		sess.Role = currentRole

		// d. Determine work directory for this role
		runWorkDir := s.Root
		if currentRole == "omega" || s.isProjectLocalRole(currentRole) {
			runWorkDir = currentWorkDir
		}

		// e. Determine model tier
		modelTier := sess.Model
		if currentRole == "done" {
			modelTier = "small"
		}

		// f. Execute LLM
		s.debugf("role_chain.exec session=%q role=%q step=%d workdir=%q model=%q", sess.ID, currentRole, step, runWorkDir, modelTier)
		stdout, stderr, code, execErr := s.execOmega(ctx, runWorkDir, modelTier, currentPromptPath, sess.SessionDir,
			streamWriter(stream, s.appendActiveStdout, activeKey),
			streamWriter(stream, s.appendActiveStderr, activeKey))

		// g. Save role log
		s.saveRoleLog(sess, step, currentRole, stdout, stderr)

		// h. Check for failure
		if code != 0 || strings.TrimSpace(stderr) != "" || execErr != nil {
			sess.Status = sessionStatusFailed
			sess.LastError = firstNonEmpty(strings.TrimSpace(stderr), errString(execErr), fmt.Sprintf("role %s fallo con codigo %d", currentRole, code))
			s.debugf("role_chain.fail session=%q role=%q rc=%d err=%q", sess.ID, currentRole, code, sess.LastError)
			return fmt.Errorf("role %s fallo para sesion %s", currentRole, sess.ID)
		}

		// i-j. Parse next role
		nextRole, found := parseNextRole(stdout, knownRoles)
		if !found {
			nextRole = "judge"
		}
		s.debugf("role_chain.next session=%q current=%q next=%q", sess.ID, currentRole, nextRole)

		// k. Special pre-actions before running next LLM
		switch nextRole {
		case "crit-error":
			_ = writeStop(s.Root, fmt.Sprintf("crit-error señalado por %s", currentRole))
			sess.Status = sessionStatusFailed
			sess.LastError = fmt.Sprintf("crit-error señalado por %s", currentRole)
			continue // checkpoint (a) will catch stop.md on next iteration

		case "merge", "plan":
			// Serialize merge on the plan worker channel
			capturedSess := sess
			capturedStream := stream
			capturedKey := activeKey
			resultCh := make(chan planWorkResult, 1)
			s.planWorkCh <- planWork{
				ctx: ctx,
				fn: func(ctx context.Context) (string, error) {
					return s.mergeApprovedSession(capturedSess, capturedStream, capturedKey)
				},
				resultCh: resultCh,
			}
			res := <-resultCh
			if res.err != nil {
				sess.Status = sessionStatusFailed
				sess.LastError = res.err.Error()
				return res.err
			}
			mergeResult = res.out
			currentWorkDir = s.Root // worktree has been cleaned up
		}

		// l. Terminal condition: done role completes the session
		if currentRole == "done" {
			taskName := sess.TaskName
			resultCh := make(chan planWorkResult, 1)
			s.planWorkCh <- planWork{
				ctx: ctx,
				fn: func(ctx context.Context) (string, error) {
					if task := s.findTask(taskName); task != nil {
						if err := plan.SaveTaskComplete(s.Root, task); err != nil {
							return "", err
						}
					}
					return "task marked complete", nil
				},
				resultCh: resultCh,
			}
			res := <-resultCh
			if res.err != nil {
				sess.Status = sessionStatusFailed
				sess.LastError = res.err.Error()
				return res.err
			}
			sess.Status = sessionStatusMerged
			sess.LastError = ""
			s.debugf("role_chain.done session=%q squash_commit=%q", sess.ID, sess.SquashCommit)
			return nil
		}

		// m. Prepare next role prompt
		extras := map[string]string{
			"{{previous_role}}": currentRole,
		}
		if nextRole == "judge" {
			// Capture commit before evaluating diff
			_, _, _ = s.captureSessionCommit(sess)
			diffSum, err := s.diffSummary(sess)
			if err != nil {
				diffSum = "sin diff disponible: " + err.Error()
			}
			extras["{{diff_summary}}"] = diffSum
		}
		if nextRole == "merge" || nextRole == "done" || nextRole == "plan" {
			extras["{{merge_result}}"] = mergeResult
		}

		promptPath, err := s.prepareRolePrompt(sess, nextRole, extras)
		if err != nil {
			sess.Status = sessionStatusFailed
			sess.LastError = err.Error()
			return err
		}
		currentPromptPath = promptPath

		// n. Advance
		currentRole = nextRole
		step++
	}

	// Exhausted max iterations — mark failed so LoopOnce can escalate the model.
	// Do NOT write stop.md here; the escalation logic in LoopOnce will write it
	// only after all model tiers have been exhausted.
	sess.Status = sessionStatusFailed
	sess.LastError = "max iteraciones alcanzado"
	return fmt.Errorf("max iteraciones alcanzado para sesion %s", sess.ID)
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

func promptLabel(recovery bool) string {
	if recovery {
		return "ERROR"
	}
	return "ALPHA"
}

func (s *State) findTask(name string) *plan.Task {
	for _, t := range s.Tasks {
		if t.Name == name {
			return t
		}
	}
	return nil
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
	if recovery {
		kind = "error"
	}
	tpl, err := config.ReadPrompt(kind)
	if err != nil {
		return "", err
	}
	ctx := s.buildSessionContext(sess)
	availableRoles := strings.Join(config.ListAvailableRoles(s.Root), ", ")
	content := strings.NewReplacer(
		"{{task_name}}", sess.Goal,
		"{{task_description}}", sess.Description,
		"{{task_model}}", sess.Model,
		"{{context}}", ctx,
		"{{feedback_file}}", filepath.ToSlash(filepath.Join(sess.SessionDir, "feedback.md")),
		"{{available_roles}}", availableRoles,
		"{{session_id}}", sess.ID,
		"{{session_dir}}", sess.SessionDir,
	).Replace(tpl)
	promptPath := filepath.Join(sess.SessionDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	ctxData := map[string]string{"goal": sess.Goal, "model": sess.Model, "context": ctx}
	ctxB, _ := yaml.Marshal(ctxData)
	if err := os.WriteFile(filepath.Join(sess.SessionDir, "context.yml"), ctxB, 0o644); err != nil {
		return "", err
	}
	return promptPath, nil
}

func (s *State) buildSessionContext(sess *SessionRuntime) string {
	parts := []string{}
	if gc := plan.ReadGlobalContext(s.Root); gc != "" {
		parts = append(parts, gc)
	}
	if task := s.findTask(sess.TaskName); task != nil && strings.TrimSpace(task.Body) != "" {
		parts = append(parts, task.Body)
	}
	return strings.Join(parts, "\n\n")
}

func allSessionsMerged(sessions []*SessionRuntime) bool {
	if len(sessions) == 0 {
		return true
	}
	for _, sess := range sessions {
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
	// Pre-check: if there are no differences between base and branch, the task
	// is already integrated. Skip the merge entirely to avoid git errors like
	// "not something we can merge" or "nothing to commit".
	if baseBranch := sess.BaseBranch; baseBranch != "" {
		if _, err := s.gitOutput(s.Root, "diff", "--quiet", baseBranch+"..."+sess.Branch); err == nil {
			if err := s.cleanupWorktree(sess); err != nil {
				return "", err
			}
			return "sin cambios nuevos; tarea ya integrada en la rama base", nil
		}
	}
	if _, err := s.gitOutput(s.Root, "merge", "--squash", "--no-commit", sess.Branch); err != nil {
		_, _ = s.gitOutput(s.Root, "merge", "--abort")
		return "", fmt.Errorf("squash merge fallo para %s: %w", sess.ID, err)
	}
	// Safety net: if the squash left nothing staged, the branch was already merged.
	if _, err := s.gitOutput(s.Root, "diff", "--cached", "--quiet"); err == nil {
		_, _ = s.gitOutput(s.Root, "merge", "--abort")
		if err := s.cleanupWorktree(sess); err != nil {
			return "", err
		}
		return "sin cambios nuevos; tarea ya integrada en la rama base", nil
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
	sess.WorktreePath = ""
	if sess.Branch != "" {
		if _, err := s.gitOutput(s.Root, "branch", "-D", sess.Branch); err != nil {
			return err
		}
		sess.Branch = ""
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
	tasks, err := plan.LoadTasks(s.Root)
	if err != nil {
		s.debugf("reload_state.plan_load.error err=%q", err)
		return err
	}
	s.Tasks = tasks
	rt, rtPath, err := loadRuntime(s.Root, tasks)
	if err != nil {
		s.debugf("reload_state.runtime_load.error err=%q", err)
		return err
	}
	s.Runtime = rt
	s.RuntimePath = rtPath
	s.detectGit()
	s.debugf("reload_state.complete runtime_path=%q summary=%s", s.RuntimePath, s.runtimeSummary())
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
	parts := make([]string, 0, len(s.Runtime.Sessions))
	for _, sess := range s.Runtime.Sessions {
		if sess == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%d:%s", sess.ID, sess.Status, sess.Attempts, sess.ReviewVerdict))
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

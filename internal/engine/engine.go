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

// maxOmegaRounds is the safety-net cap on alpha→omega loop iterations per session.
// It is a var (not const) so tests can lower it to exercise the exhaustion path quickly.
var maxOmegaRounds = 20

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
	return st, nil
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

	// Ensure fase0 session exists when the plan has not been evaluated yet.
	meta, _ := plan.LoadPlanMeta(s.Root)
	if !meta.Fase0Complete && !s.hasFase0Session() {
		s.ensureFase0Session()
		s.debugf("loop_once.fase0_session_created")
		if err := s.saveRuntime(); err != nil {
			return err
		}
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
	sess.Round = 0
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

	workDir := s.Root

	omegaDir := filepath.Join(sess.SessionDir, "omega")
	if err := os.MkdirAll(omegaDir, 0o755); err != nil {
		return RunRecord{TaskName: sess.ID, Err: err}, err
	}

	promptPath, err := s.prepareSessionPrompt(sess, recovery)
	if err != nil {
		sess.Status = sessionStatusFailed
		sess.LastError = err.Error()
		s.debugf("run_session.prepare_prompt.error session=%q err=%q", sess.ID, err)
		return RunRecord{TaskName: sess.ID, Err: err}, err
	}

	s.debugf("run_session.workspace_ready session=%q workdir=%q omegadir=%q", sess.ID, workDir, omegaDir)

	// Alpha→Omegas loop
	for i := 0; i < maxOmegaRounds; i++ {
		if hasStop(s.Root) {
			sess.Status = sessionStatusFailed
			sess.LastError = "stop.md detectado"
			return RunRecord{ID: runID, TaskName: sess.ID, Code: 1, Model: sess.Model, Err: fmt.Errorf("stop.md detectado")}, fmt.Errorf("stop.md detectado durante sesion %s", sess.ID)
		}

		roundSubdir := fmt.Sprintf("round-%d", sess.Round)

		// Determine prompt label for this round
		currentPlannerKind := plannerKind
		currentPlannerLabel := plannerLabel
		if i > 0 {
			// Re-evaluation rounds always use alpha mode
			currentPlannerKind = "alpha"
			currentPlannerLabel = "ALPHA"
		}

		if stream {
			s.appendActiveStdout(activeKey, fmt.Sprintf("=== %s (round %d): ejecutando ===\n", currentPlannerLabel, sess.Round))
		}

		alphaStdout, alphaStderr, alphaCode, alphaErr := s.execOmega(ctx, workDir, sess.Model, promptPath, sess.SessionDir,
			streamWriter(stream, s.appendActiveStdout, activeKey),
			streamWriter(stream, s.appendActiveStderr, activeKey))
		s.debugf("run_session.alpha_complete session=%q round=%d rc=%d err=%q stderr_nonempty=%t", sess.ID, sess.Round, alphaCode, errString(alphaErr), strings.TrimSpace(alphaStderr) != "")

		s.saveRoleLog(sess, 0, currentPlannerKind, roundSubdir, alphaStdout, alphaStderr)

		if alphaCode != 0 || strings.TrimSpace(alphaStderr) != "" || alphaErr != nil {
			rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: fmt.Sprintf("=== %s ===\n%s", currentPlannerLabel, alphaStdout), Stderr: fmt.Sprintf("=== %s ===\n%s", currentPlannerLabel, alphaStderr), Code: alphaCode, Model: sess.Model, Err: alphaErr}
			sess.Status = sessionStatusFailed
			sess.LastError = firstNonEmpty(strings.TrimSpace(alphaStderr), errString(alphaErr), fmt.Sprintf("returncode %d", alphaCode))
			_ = s.persistRun(rr)
			return rr, fmt.Errorf("%s fallo para sesion %s", strings.ToLower(currentPlannerLabel), sess.ID)
		}

		// Scan omega dir for *.md files
		entries, err := os.ReadDir(omegaDir)
		if err != nil && !os.IsNotExist(err) {
			sess.Status = sessionStatusFailed
			sess.LastError = err.Error()
			return RunRecord{ID: runID, TaskName: sess.ID, Code: 1, Model: sess.Model, Err: err}, err
		}

		var omegaFiles []string
		var hasDone bool
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if e.Name() == "done.md" {
				hasDone = true
			} else if strings.HasSuffix(e.Name(), ".md") {
				omegaFiles = append(omegaFiles, filepath.Join(omegaDir, e.Name()))
			}
		}

		if !hasDone && len(omegaFiles) == 0 {
			sess.Status = sessionStatusFailed
			sess.LastError = "alpha no genero ficheros omega"
			rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: alphaStdout, Stderr: "alpha no genero ficheros omega", Code: 1, Model: sess.Model}
			_ = s.persistRun(rr)
			return rr, fmt.Errorf("alpha no genero ficheros omega para sesion %s", sess.ID)
		}

		if hasDone && len(omegaFiles) == 0 {
			// Only done.md → session complete
			_ = os.Remove(filepath.Join(omegaDir, "done.md"))
			if sess.Kind == "fase0" {
				if err := plan.WriteFase0Complete(s.Root); err != nil {
					s.debugf("run_session.fase0_complete.write_error session=%q err=%q", sess.ID, err)
				}
			} else if task := s.findTask(sess.TaskName); task != nil {
				if err := plan.SaveTaskComplete(s.Root, task); err != nil {
					sess.Status = sessionStatusFailed
					sess.LastError = err.Error()
					return RunRecord{ID: runID, TaskName: sess.ID, Code: 1, Model: sess.Model, Err: err}, err
				}
			}
			sess.Status = sessionStatusMerged
			sess.LastError = ""
			s.debugf("run_session.done session=%q round=%d", sess.ID, sess.Round)
			rr := RunRecord{ID: runID, TaskName: sess.ID, Stdout: fmt.Sprintf("=== %s ===\n%s\n\n[sesion completada en round %d]", currentPlannerLabel, alphaStdout, sess.Round), Code: 0, Model: sess.Model}
			if stream {
				s.setActiveRunResult(activeKey, rr)
			}
			_ = s.persistRun(rr)
			return rr, nil
		}

		// Archive omega files to omega-archive/round-N/ before executing
		archiveDir := filepath.Join(sess.SessionDir, "omega-archive", fmt.Sprintf("round-%d", sess.Round))
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			sess.Status = sessionStatusFailed
			sess.LastError = err.Error()
			return RunRecord{ID: runID, TaskName: sess.ID, Code: 1, Model: sess.Model, Err: err}, err
		}

		// Move omega files to archive and update paths
		archivedFiles := make([]string, 0, len(omegaFiles))
		for _, f := range omegaFiles {
			dest := filepath.Join(archiveDir, filepath.Base(f))
			if err := os.Rename(f, dest); err != nil {
				sess.Status = sessionStatusFailed
				sess.LastError = err.Error()
				return RunRecord{ID: runID, TaskName: sess.ID, Code: 1, Model: sess.Model, Err: err}, err
			}
			archivedFiles = append(archivedFiles, dest)
		}
		// Remove done.md if it was alongside omega files (shouldn't normally happen)
		if hasDone {
			_ = os.Remove(filepath.Join(omegaDir, "done.md"))
		}

		// Execute all omega files in parallel; collect errors but don't cancel others
		var wg sync.WaitGroup
		var omegaMu sync.Mutex
		var omegaErrs []error
		for _, f := range archivedFiles {
			f := f
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.executeOmegaFile(ctx, sess, f, roundSubdir, stream, activeKey); err != nil {
					omegaMu.Lock()
					omegaErrs = append(omegaErrs, err)
					omegaMu.Unlock()
				}
			}()
		}
		wg.Wait()

		if len(omegaErrs) > 0 {
			// Log errors but don't fail immediately — alpha will re-evaluate
			msgs := make([]string, 0, len(omegaErrs))
			for _, e := range omegaErrs {
				msgs = append(msgs, e.Error())
			}
			s.debugf("run_session.omega_errors session=%q round=%d errors=%q", sess.ID, sess.Round, strings.Join(msgs, "; "))
		}

		sess.Round++
		s.debugf("run_session.round_complete session=%q next_round=%d", sess.ID, sess.Round)
	}

	// Exhausted max rounds
	sess.Status = sessionStatusFailed
	sess.LastError = "max rondas omega alcanzado"
	rr := RunRecord{ID: runID, TaskName: sess.ID, Code: 1, Model: sess.Model, Err: fmt.Errorf("max rondas omega alcanzado para sesion %s", sess.ID)}
	_ = s.persistRun(rr)
	return rr, fmt.Errorf("max rondas omega alcanzado para sesion %s", sess.ID)
}

// executeOmegaFile executes a single omega file as an LLM agent.
// The file may contain YAML frontmatter to specify the model tier.
// Logs go to roles/<roundLogDir>/<filename>.log.
func (s *State) executeOmegaFile(ctx context.Context, sess *SessionRuntime, omegaFilePath, roundLogDir string, stream bool, activeKey string) error {
	content, err := os.ReadFile(omegaFilePath)
	if err != nil {
		return fmt.Errorf("leer fichero omega %s: %w", omegaFilePath, err)
	}

	// Parse optional YAML frontmatter for model override
	modelTier := sess.Model
	if fm := parseFrontmatterModel(string(content)); fm != "" {
		modelTier = fm
	}

	filename := strings.TrimSuffix(filepath.Base(omegaFilePath), ".md")
	s.debugf("execute_omega_file session=%q file=%q model=%q round_log=%q", sess.ID, filename, modelTier, roundLogDir)

	stdout, stderr, code, execErr := s.execOmega(ctx, s.Root, modelTier, omegaFilePath, sess.SessionDir,
		streamWriter(stream, s.appendActiveStdout, activeKey),
		streamWriter(stream, s.appendActiveStderr, activeKey))

	// Log to roles/<roundLogDir>/<filename>.log (no step prefix for omega files)
	logDir := filepath.Join(sess.SessionDir, "roles", roundLogDir)
	_ = os.MkdirAll(logDir, 0o755)
	logBody := stdout
	if strings.TrimSpace(stderr) != "" {
		logBody += "\n--- STDERR ---\n" + stderr
	}
	_ = os.WriteFile(filepath.Join(logDir, filename+".log"), []byte(logBody), 0o644)

	if code != 0 || strings.TrimSpace(stderr) != "" || execErr != nil {
		return fmt.Errorf("omega %s fallo (rc=%d): %s", filename, code, firstNonEmpty(strings.TrimSpace(stderr), errString(execErr)))
	}
	return nil
}

// parseFrontmatterModel extracts the "model" field from YAML frontmatter (--- ... ---).
// Returns "" if no frontmatter or no model field.
func parseFrontmatterModel(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	// Find closing ---
	rest := content[3:]
	// Skip optional newline after opening ---
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	} else if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	}
	end := strings.Index(rest, "---")
	if end < 0 {
		return ""
	}
	fmContent := rest[:end]
	var fm struct {
		Model string `yaml:"model"`
	}
	if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
		return ""
	}
	return strings.TrimSpace(fm.Model)
}

// saveRoleLog writes role execution output to loop/sessions/<id>/roles/[subdir/]<NNN>-<role>.log.
func (s *State) saveRoleLog(sess *SessionRuntime, step int, role, subdir, stdout, stderr string) {
	logDir := filepath.Join(sess.SessionDir, "roles")
	if subdir != "" {
		logDir = filepath.Join(logDir, subdir)
	}
	_ = os.MkdirAll(logDir, 0o755)
	name := fmt.Sprintf("%03d-%s.log", step, role)
	body := stdout
	if strings.TrimSpace(stderr) != "" {
		body += "\n--- STDERR ---\n" + stderr
	}
	_ = os.WriteFile(filepath.Join(logDir, name), []byte(body), 0o644)
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
	} else if sess.Kind == "fase0" {
		kind = "fase0"
	}
	tpl, err := config.ReadPrompt(s.Root, kind)
	if err != nil {
		return "", err
	}
	ctx := s.buildSessionContext(sess)
	availableRoles := strings.Join(config.ListAvailableRoles(s.Root), ", ")
	availableSkills := readSkillsIndex(s.Root)
	omegaDir := filepath.Join(sess.SessionDir, "omega")
	content := strings.NewReplacer(
		"{{task_name}}", sess.Goal,
		"{{task_description}}", sess.Description,
		"{{task_model}}", sess.Model,
		"{{context}}", ctx,
		"{{feedback_file}}", filepath.ToSlash(filepath.Join(sess.SessionDir, "feedback.md")),
		"{{available_roles}}", availableRoles,
		"{{available_skills}}", availableSkills,
		"{{session_id}}", sess.ID,
		"{{session_dir}}", sess.SessionDir,
		"{{omega_dir}}", omegaDir,
		"{{plan_dir}}", filepath.Join(s.Root, "plan"),
		"{{sessions_dir}}", filepath.Join(s.Root, "loop", "sessions"),
		"{{skills_dir}}", filepath.Join(s.Root, "loop", "skills"),
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

func readSkillsIndex(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "loop", "skills", "index.md"))
	if err != nil {
		return "(ninguna skill disponible aún)"
	}
	return strings.TrimSpace(string(b))
}

func (s *State) hasFase0Session() bool {
	for _, sess := range s.Runtime.Sessions {
		if sess.Kind == "fase0" {
			return true
		}
	}
	return false
}

func (s *State) ensureFase0Session() {
	now := time.Now().Format(time.RFC3339)
	fase0 := &SessionRuntime{
		ID:         "fase0",
		Kind:       "fase0",
		Goal:       "Fase 0: evaluación y preparación del plan",
		Model:      "large",
		Status:     sessionStatusPlanned,
		SessionDir: filepath.Join(s.Root, "loop", "sessions", "fase0"),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	s.Runtime.Sessions = append([]*SessionRuntime{fase0}, s.Runtime.Sessions...)
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

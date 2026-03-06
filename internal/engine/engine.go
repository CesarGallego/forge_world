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
	"forgeworld/internal/gitflow"
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
	Root       string
	Config     *config.Config
	PlanPath   string
	Plan       *plan.Plan
	StatusLine string

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
	if changed := plan.EnsurePhase0(p); changed {
		if err := plan.Save(path, p); err != nil {
			return nil, err
		}
	}
	return &State{Root: root, Config: cfg, PlanPath: path, Plan: p}, nil
}

func (s *State) Tree() string {
	s.mu.RLock()
	activeByTask := make(map[string]struct{}, len(s.activeRuns))
	for _, key := range s.activeOrder {
		if r := s.activeRuns[key]; r != nil && strings.TrimSpace(r.TaskName) != "" {
			activeByTask[r.TaskName] = struct{}{}
		}
	}
	s.mu.RUnlock()

	var b strings.Builder
	for pi, phase := range s.Plan.Phases {
		mark := "[ ]"
		if phase.Complete {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%s F%d %s\n", mark, pi+1, phase.Name)
		for ni, node := range phase.Tasks {
			if node.Task != nil {
				t := node.Task
				tm := "[ ]"
				if t.Complete {
					tm = "[x]"
				} else if _, ok := activeByTask[t.Name]; ok {
					tm = "[>]"
				}
				fmt.Fprintf(&b, "  %s T%d.%d %s (%s)\n", tm, pi+1, ni+1, t.Name, t.Model)
				continue
			}
			fmt.Fprintf(&b, "  [||] T%d.%d paralelo\n", pi+1, ni+1)
			for ti, t := range node.Parallel {
				tm := "[ ]"
				if t.Complete {
					tm = "[x]"
				} else if _, ok := activeByTask[t.Name]; ok {
					tm = "[>]"
				}
				fmt.Fprintf(&b, "    %s P%d %s (%s)\n", tm, ti+1, t.Name, t.Model)
			}
		}
	}
	return b.String()
}

func (s *State) LoopOnce(ctx context.Context) error {
	if err := s.reloadPlan(); err != nil {
		return err
	}
	if hasStop(s.Root) {
		return fmt.Errorf("se encontro loop/stop.md; revisa bloqueo antes de continuar")
	}
	a, b, isPair, ok := plan.NextNode(s.Plan)
	if !ok {
		s.StatusLine = "Plan completado."
		return plan.Save(s.PlanPath, s.Plan)
	}
	s.StatusLine = ""
	if isPair {
		runs, err := s.runParallel(ctx, a, b)
		s.setLastRuns(runs)
		if err != nil {
			return err
		}
	} else {
		r, err := s.runTask(ctx, a, s.Root, true)
		s.setLastRuns([]RunRecord{r})
		if err != nil {
			return err
		}
	}
	if err := plan.Save(s.PlanPath, s.Plan); err != nil {
		return err
	}
	if hasStop(s.Root) {
		return fmt.Errorf("se genero loop/stop.md; flujo detenido")
	}
	return nil
}

func hasStop(root string) bool {
	_, err := os.Stat(filepath.Join(root, "loop", "stop.md"))
	return err == nil
}

func (s *State) runParallel(ctx context.Context, a, b plan.TaskRef) ([]RunRecord, error) {
	base, err := gitflow.CurrentBranch(s.Root)
	if err != nil {
		writeStop(s.Root, err.Error())
		return nil, err
	}
	leftTask := plan.ResolveTask(s.Plan, a)
	rightTask := plan.ResolveTask(s.Plan, b)

	wtA, err := gitflow.Create(s.Root, leftTask.Name)
	if err != nil {
		writeStop(s.Root, err.Error())
		return nil, err
	}
	wtB, err := gitflow.Create(s.Root, rightTask.Name)
	if err != nil {
		_ = gitflow.Cleanup(s.Root, wtA)
		writeStop(s.Root, err.Error())
		return nil, err
	}
	defer func() {
		_ = gitflow.Cleanup(s.Root, wtA)
		_ = gitflow.Cleanup(s.Root, wtB)
	}()

	var wg sync.WaitGroup
	var runA, runB RunRecord
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		runA, errA = s.runTask(ctx, a, wtA.Path, true)
	}()
	go func() {
		defer wg.Done()
		runB, errB = s.runTask(ctx, b, wtB.Path, true)
	}()
	wg.Wait()

	if errA != nil || errB != nil {
		return []RunRecord{runA, runB}, fmt.Errorf("fallo en ejecucion paralela")
	}
	if err := gitflow.Merge(s.Root, base, []string{wtA.Branch, wtB.Branch}); err != nil {
		writeStop(s.Root, "conflicto de merge en paralelo: "+err.Error())
		return []RunRecord{runA, runB}, err
	}
	plan.MarkDone(s.Plan, a)
	plan.MarkDone(s.Plan, b)
	return []RunRecord{runA, runB}, nil
}

func (s *State) runTask(ctx context.Context, ref plan.TaskRef, workDir string, stream bool) (RunRecord, error) {
	completedBefore := snapshotCompletedTasks(s.Plan)
	t, ok := plan.TryResolveTask(s.Plan, ref)
	if !ok {
		return RunRecord{}, fmt.Errorf("referencia de tarea invalida")
	}
	taskName := t.Name
	if t.Complete {
		return RunRecord{TaskName: t.Name}, nil
	}
	baseModel := t.Model
	effectiveModel := baseModel
	if strings.TrimSpace(t.State.EffectiveModel) != "" {
		effectiveModel = t.State.EffectiveModel
	}
	failedBefore := t.State.LastReturnCode != 0 || strings.TrimSpace(t.State.LastError) != ""
	if failedBefore {
		next, changed, _ := plan.EscalateModel(effectiveModel)
		if changed {
			effectiveModel = next
		}
		feedbackPath, _ := s.appendFeedback(ref, fmt.Sprintf("Fallo previo rc=%d error=%s", t.State.LastReturnCode, t.State.LastError))
		t.State.FeedbackRef = feedbackPath
		if strings.EqualFold(effectiveModel, plan.ModelLarge) && t.State.Attempts >= 3 {
			msg := "No se puede continuar: tarea fallo repetidamente en modelo large"
			_ = writeStop(s.Root, msg)
			return RunRecord{TaskName: t.Name, Err: fmt.Errorf(msg)}, fmt.Errorf(msg)
		}
	}

	alphaPromptPath, taskDir, err := s.preparePrompt(ref, failedBefore)
	if err != nil {
		return RunRecord{TaskName: t.Name, Err: err}, err
	}
	if err := s.reloadPlan(); err != nil {
		return RunRecord{TaskName: t.Name, Err: err}, err
	}
	restoreCompletedTasks(s.Plan, completedBefore)
	if updatedRef, ok := s.resolveTaskRefByName(taskName, ref); ok {
		ref = updatedRef
	}

	runID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), plan.TaskSlug(t.Name))
	activeKey := fmt.Sprintf("%d/%d/%d", ref.PhaseIdx, ref.NodeIdx, ref.TaskIdx)
	if stream {
		s.setActiveRun(activeKey, RunRecord{
			ID:       runID,
			TaskName: t.Name,
			Model:    effectiveModel,
		})
		defer s.clearActiveRun(activeKey)
	}
	if stream {
		s.appendActiveStdout(activeKey, "=== ALPHA: generando prompt omega ===\n")
	}
	alphaStdout, alphaStderr, alphaCode, alphaErr := s.execOmega(
		ctx,
		workDir,
		effectiveModel,
		alphaPromptPath,
		taskDir,
		func(chunk string) {
			if stream {
				s.appendActiveStdout(activeKey, chunk)
			}
		},
		func(chunk string) {
			if stream {
				s.appendActiveStderr(activeKey, chunk)
			}
		},
	)
	if alphaCode != 0 || strings.TrimSpace(alphaStderr) != "" || alphaErr != nil {
		rr := RunRecord{
			ID:       runID,
			TaskName: t.Name,
			Stdout:   "=== ALPHA ===\n" + alphaStdout,
			Stderr:   "=== ALPHA ===\n" + alphaStderr,
			Code:     alphaCode,
			Model:    effectiveModel,
			Err:      alphaErr,
		}
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		if err := s.persistRun(rr); err != nil {
			rr.Err = fmt.Errorf("%w; persistencia de logs fallo: %v", alphaErr, err)
		}
		return rr, fmt.Errorf("alpha fallo para tarea %s", t.Name)
	}

	omegaPrompt := strings.TrimSpace(alphaStdout)
	if omegaPrompt == "" {
		rr := RunRecord{
			ID:       runID,
			TaskName: t.Name,
			Stdout:   "=== ALPHA ===\n" + alphaStdout,
			Stderr:   "alpha no genero prompt omega (stdout vacio)",
			Code:     1,
			Model:    effectiveModel,
			Err:      fmt.Errorf("alpha no genero prompt omega"),
		}
		if stream {
			s.setActiveRunResult(activeKey, rr)
		}
		if err := s.persistRun(rr); err != nil {
			rr.Err = fmt.Errorf("%w; persistencia de logs fallo: %v", rr.Err, err)
		}
		return rr, rr.Err
	}
	omegaPromptPath := filepath.Join(taskDir, "omega.md")
	if err := os.WriteFile(omegaPromptPath, []byte(omegaPrompt+"\n"), 0o644); err != nil {
		return RunRecord{TaskName: t.Name, Err: err}, err
	}

	if stream {
		s.appendActiveStdout(activeKey, "\n=== OMEGA: ejecutando tarea ===\n")
	}
	omegaStdout, omegaStderr, omegaCode, omegaErr := s.execOmega(
		ctx,
		workDir,
		effectiveModel,
		omegaPromptPath,
		taskDir,
		func(chunk string) {
			if stream {
				s.appendActiveStdout(activeKey, chunk)
			}
		},
		func(chunk string) {
			if stream {
				s.appendActiveStderr(activeKey, chunk)
			}
		},
	)
	orderStdout := ""
	orderStderr := ""
	orderCode := 0
	var orderErr error
	if stream {
		s.appendActiveStdout(activeKey, "\n=== ORDENANAMIENTO: validando/corrigiendo plan ===\n")
	}
	orderStdout, orderStderr, orderCode, orderErr = s.runOrdenanamiento(
		ctx,
		workDir,
		effectiveModel,
		taskDir,
		func(chunk string) {
			if stream {
				s.appendActiveStdout(activeKey, chunk)
			}
		},
		func(chunk string) {
			if stream {
				s.appendActiveStderr(activeKey, chunk)
			}
		},
	)

	stdoutParts := []string{
		"=== ALPHA ===\n" + alphaStdout,
		"=== OMEGA ===\n" + omegaStdout,
		"=== ORDENANAMIENTO ===\n" + orderStdout,
	}
	stderrParts := []string{
		"=== ALPHA ===\n" + alphaStderr,
		"=== OMEGA ===\n" + omegaStderr,
		"=== ORDENANAMIENTO ===\n" + orderStderr,
	}
	finalCode := omegaCode
	finalErr := omegaErr
	if orderCode != 0 || strings.TrimSpace(orderStderr) != "" || orderErr != nil {
		finalCode = orderCode
		finalErr = orderErr
	}
	stdout := strings.Join(stdoutParts, "\n\n")
	stderr := strings.Join(stderrParts, "\n\n")
	rr := RunRecord{ID: runID, TaskName: t.Name, Stdout: stdout, Stderr: stderr, Code: finalCode, Model: effectiveModel, Err: finalErr}
	if stream {
		s.setActiveRunResult(activeKey, rr)
	}
	if err := s.persistRun(rr); err != nil {
		rr.Err = fmt.Errorf("%w; persistencia de logs fallo: %v", omegaErr, err)
	}
	if err := s.reloadPlan(); err != nil {
		return rr, err
	}
	restoreCompletedTasks(s.Plan, completedBefore)
	if updatedRef, ok := s.resolveTaskRefByName(taskName, ref); ok {
		ref = updatedRef
	}
	t, ok = plan.TryResolveTask(s.Plan, ref)
	if !ok {
		return rr, fmt.Errorf("no se pudo resolver la tarea tras recargar plan.yml")
	}

	omegaMarkedDone := strings.Contains(omegaStdout, omegaCompletionMarker)
	orderOK := orderCode == 0 && strings.TrimSpace(orderStderr) == "" && orderErr == nil
	if omegaCode == 0 && strings.TrimSpace(omegaStderr) == "" && omegaErr == nil && omegaMarkedDone && orderOK {
		plan.MarkDone(s.Plan, ref)
		t.State.LastReturnCode = 0
		t.State.LastError = ""
		t.State.Attempts++
		t.State.EffectiveModel = effectiveModel
		return rr, nil
	}
	t.State.Attempts++
	t.State.LastReturnCode = finalCode
	if !orderOK {
		if strings.TrimSpace(orderStderr) != "" {
			t.State.LastError = strings.TrimSpace(orderStderr)
		} else if orderErr != nil {
			t.State.LastError = orderErr.Error()
		} else {
			t.State.LastError = fmt.Sprintf("ordenanamiento fallo con returncode %d", orderCode)
		}
	} else if omegaCode == 0 && strings.TrimSpace(omegaStderr) == "" && omegaErr == nil && !omegaMarkedDone {
		t.State.LastError = fmt.Sprintf("omega no confirmo finalizacion; falta marcador %q", omegaCompletionMarker)
	} else if strings.TrimSpace(omegaStderr) != "" {
		t.State.LastError = strings.TrimSpace(omegaStderr)
	} else if omegaErr != nil {
		t.State.LastError = omegaErr.Error()
	} else {
		t.State.LastError = fmt.Sprintf("returncode %d", omegaCode)
	}
	t.State.EffectiveModel = effectiveModel
	if updatedRef, ok := s.resolveTaskRefByName(taskName, ref); ok {
		ref = updatedRef
	}
	_, _ = s.appendFeedback(ref, t.State.LastError)
	return rr, fmt.Errorf("tarea %s fallo", t.Name)
}

func (s *State) appendFeedback(ref plan.TaskRef, msg string) (string, error) {
	t := plan.ResolveTask(s.Plan, ref)
	id := plan.TaskSlug(t.Name)
	dir := filepath.Join(s.Root, "loop", "tasks", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "feedback.md")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n- %s: %s\n", time.Now().Format(time.RFC3339), msg)
	return path, err
}

func (s *State) preparePrompt(ref plan.TaskRef, wasError bool) (string, string, error) {
	t := plan.ResolveTask(s.Plan, ref)
	id := plan.TaskSlug(t.Name)
	taskDir := filepath.Join(s.Root, "loop", "tasks", id)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return "", "", err
	}
	kind := "alpha"
	if ref.PhaseIdx == 0 {
		kind = "phase0"
	}
	if wasError {
		et, err := config.ReadPrompt("error")
		if err != nil {
			return "", "", err
		}
		if _, err := s.appendFeedback(ref, "Aplicado prompt de error"); err != nil {
			return "", "", err
		}
		_ = et
	}
	tpl, err := config.ReadPrompt(kind)
	if err != nil {
		return "", "", err
	}
	ctx := plan.BuildContext(s.Plan, ref)
	content := buildPrompt(tpl, t, ctx)
	promptPath := filepath.Join(taskDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", "", err
	}
	ctxData := map[string]string{"context": ctx, "task": t.Name, "model": t.Model}
	ctxB, _ := yaml.Marshal(ctxData)
	if err := os.WriteFile(filepath.Join(taskDir, "context.yml"), ctxB, 0o644); err != nil {
		return "", "", err
	}
	if ref.PhaseIdx == 0 {
		_ = ensureSkillSeed(s.Root)
	}
	return promptPath, taskDir, nil
}

func ensureSkillSeed(root string) error {
	base := filepath.Join(root, "loop", "skills")
	_ = os.MkdirAll(base, 0o755)
	files := map[string]string{
		"frontend.md":          "# Skill Frontend\n\nTareas comunes de interfaz y navegacion.\n",
		"frontend/nav_tree.md": "Revisar que toda pagina sea accesible desde alguna ruta de navegacion.\n",
	}
	for rel, body := range files {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildPrompt(template string, t *plan.Task, context string) string {
	replacer := strings.NewReplacer(
		"{{task_name}}", t.Name,
		"{{task_description}}", t.Description,
		"{{task_model}}", t.Model,
		"{{context}}", context,
	)
	return replacer.Replace(template)
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
	p, _, err := plan.Load(workDir)
	if err != nil {
		return "", err.Error(), 1, err
	}
	errs := plan.Validate(p)
	if len(errs) == 0 {
		return "plan/plan.yml valido", "", 0, nil
	}

	tpl, err := config.ReadPrompt("ordenanamiento")
	if err != nil {
		return "", err.Error(), 1, err
	}
	validation := formatValidationErrors(errs)
	content := strings.NewReplacer(
		"{{validation_errors}}", validation,
	).Replace(tpl)
	promptPath := filepath.Join(taskDir, "ordenanamiento.md")
	if err := os.WriteFile(promptPath, []byte(content), 0o644); err != nil {
		return "", err.Error(), 1, err
	}
	out, errOut, code, runErr := s.execOmega(ctx, workDir, modelTier, promptPath, taskDir, onStdout, onStderr)
	if code != 0 || strings.TrimSpace(errOut) != "" || runErr != nil {
		return out, errOut, code, runErr
	}

	after, _, err := plan.Load(workDir)
	if err != nil {
		return out, err.Error(), 1, err
	}
	afterErrs := plan.Validate(after)
	if len(afterErrs) > 0 {
		msg := "plan sigue invalido tras ordenanamiento:\n" + formatValidationErrors(afterErrs)
		return out, msg, 1, fmt.Errorf("plan invalido tras ordenanamiento")
	}
	return out, errOut, 0, nil
}

func (s *State) Fix(ctx context.Context) (RunRecord, error) {
	if err := s.reloadPlan(); err != nil {
		return RunRecord{TaskName: "ordenanamiento", Err: err}, err
	}
	taskDir := filepath.Join(s.Root, "loop", "tasks", "ordenanamiento")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return RunRecord{TaskName: "ordenanamiento", Err: err}, err
	}
	runID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), "ordenanamiento")
	stdout, stderr, code, runErr := s.runOrdenanamiento(ctx, s.Root, plan.ModelMedium, taskDir, nil, nil)
	rr := RunRecord{
		ID:       runID,
		TaskName: "ordenanamiento",
		Stdout:   "=== ORDENANAMIENTO ===\n" + stdout,
		Stderr:   "=== ORDENANAMIENTO ===\n" + stderr,
		Code:     code,
		Model:    plan.ModelMedium,
		Err:      runErr,
	}
	if err := s.persistRun(rr); err != nil {
		rr.Err = fmt.Errorf("%w; persistencia de logs fallo: %v", runErr, err)
	}
	if err := s.reloadPlan(); err != nil {
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

func (s *State) reloadPlan() error {
	p, path, err := plan.Load(s.Root)
	if err != nil {
		return err
	}
	if changed := plan.EnsurePhase0(p); changed {
		if err := plan.Save(path, p); err != nil {
			return err
		}
	}
	s.Plan = p
	s.PlanPath = path
	return nil
}

func (s *State) resolveTaskRefByName(name string, fallback plan.TaskRef) (plan.TaskRef, bool) {
	if ref, ok := plan.FindTaskRefByName(s.Plan, name); ok {
		return ref, true
	}
	if _, ok := plan.TryResolveTask(s.Plan, fallback); ok {
		return fallback, true
	}
	return plan.TaskRef{}, false
}

func snapshotCompletedTasks(p *plan.Plan) map[string]struct{} {
	done := make(map[string]struct{})
	for pi := range p.Phases {
		phase := &p.Phases[pi]
		for ni := range phase.Tasks {
			node := &phase.Tasks[ni]
			if node.Task != nil {
				if node.Task.Complete {
					done[node.Task.Name] = struct{}{}
				}
				continue
			}
			for ti := range node.Parallel {
				if node.Parallel[ti].Complete {
					done[node.Parallel[ti].Name] = struct{}{}
				}
			}
		}
	}
	return done
}

func restoreCompletedTasks(p *plan.Plan, done map[string]struct{}) {
	if len(done) == 0 {
		return
	}
	for pi := range p.Phases {
		phase := &p.Phases[pi]
		for ni := range phase.Tasks {
			node := &phase.Tasks[ni]
			if node.Task != nil {
				if _, ok := done[node.Task.Name]; ok {
					node.Task.Complete = true
				}
				continue
			}
			for ti := range node.Parallel {
				if _, ok := done[node.Parallel[ti].Name]; ok {
					node.Parallel[ti].Complete = true
				}
			}
		}
	}
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

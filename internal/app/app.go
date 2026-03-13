package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"forgeworld/internal/bootstrap"
	"forgeworld/internal/config"
	"forgeworld/internal/engine"
	"forgeworld/internal/plan"
	"forgeworld/internal/ui"
)

func Run(args []string) error {
	if len(args) < 2 {
		fmt.Print(helpText())
		return nil
	}
	cmd := strings.TrimSpace(args[1])
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		if len(args) >= 3 {
			return printCommandHelp(args[2])
		}
		fmt.Print(helpText())
		return nil
	}
	switch cmd {
	case "init":
		if hasHelpFlag(args[2:]) {
			return printCommandHelp("init")
		}
		return runInit(cwd, args[2:])
	case "validate":
		if hasHelpFlag(args[2:]) {
			return printCommandHelp("validate")
		}
		return runValidate(cwd)
	case "tui":
		if hasHelpFlag(args[2:]) {
			return printCommandHelp("tui")
		}
		return runTUI(cwd)
	default:
		return usage()
	}
}

func printCommandHelp(cmd string) error {
	txt, ok := subcommandHelp(strings.TrimSpace(cmd))
	if !ok {
		return errors.New("subcomando de ayuda desconocido\n\n" + helpText())
	}
	fmt.Print(txt)
	return nil
}

func usage() error {
	return errors.New("comando desconocido o uso invalido\n\n" + helpText())
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}

func subcommandHelp(cmd string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "init":
		return `forgeworld init - Inicializa estructura y configuracion

USO
  forgeworld init [--executor codex|claude|gemini] [--recreate]

DESCRIPCION
  Crea estructura base (plan/tasks/, loop/...) y, si falta, .forgeworld.yml.
  Crea prompts globales si faltan. Con --recreate los sobrescribe.
  Tambien crea plan/prompt.md para iniciar el plan con tu agente favorito.

EJEMPLOS
  forgeworld init
  forgeworld init --executor codex
  forgeworld init --executor claude
  forgeworld init --recreate
  # Luego: crea ficheros en plan/tasks/ y ejecuta forgeworld tui
`, true
	case "validate":
		return `forgeworld validate - Valida los ficheros de tareas en plan/tasks/

USO
  forgeworld validate

DESCRIPCION
  Lee plan/tasks/*.md y valida que cada tarea tenga nombre (H1) y modelo valido.
  Si solo existe plan/plan.yml, muestra instrucciones de migracion.

EJEMPLOS
  forgeworld validate
`, true
	case "tui":
		return `forgeworld tui - Inicia interfaz interactiva

USO
  forgeworld tui

DESCRIPCION
  Ejecuta el bucle alpha/omega y muestra estado de tareas y logs live.
  Incluye navegacion de tareas, inspeccion de stdout/stderr y scroll interno.

EJEMPLOS
  forgeworld tui
`, true
	case "help":
		return `forgeworld help - Ayuda general o por subcomando

USO
  forgeworld help
  forgeworld help <init|validate|tui>
`, true
	default:
		return "", false
	}
}

func helpText() string {
	return `forgeworld - Orquestador de tareas alpha/omega

USO
  forgeworld <comando> [opciones]

COMANDOS
  init [--executor codex|claude|gemini] [--recreate]
      Inicializa estructura de proyecto y configuracion base.
      Crea plan/prompt.md para arrancar planificacion con tu agente.

  validate
      Valida plan/tasks/*.md y muestra lista de tareas.

  tui
      Abre interfaz interactiva para ejecutar el bucle de tareas.

  help
      Muestra esta ayuda.
      Usa 'forgeworld help <comando>' para ayuda detallada.

EJEMPLOS
  forgeworld init --executor codex
  forgeworld validate
  forgeworld tui
`
}

func runInit(root string, args []string) error {
	executorPreset, recreate, err := parseInitOptions(args)
	if err != nil {
		return err
	}
	created, err := bootstrap.EnsureLayout(root, executorPreset)
	if err != nil {
		return err
	}
	promptsUpdated, err := bootstrap.EnsurePromptFiles(recreate)
	if err != nil {
		return err
	}
	hint, err := bootstrap.EnsurePromptDirHint()
	if err != nil {
		return err
	}
	if len(created) == 0 {
		fmt.Println("Infraestructura ya existe.")
	} else {
		fmt.Println("Archivos creados:")
		for _, p := range created {
			rel, _ := filepath.Rel(root, p)
			fmt.Printf("- %s\n", rel)
		}
	}
	if len(promptsUpdated) > 0 {
		fmt.Println("Prompts actualizados:")
		for _, p := range promptsUpdated {
			fmt.Printf("- %s\n", p)
		}
	}
	fmt.Println(hint)
	fmt.Println("Siguiente paso:")
	fmt.Println("1) Crea ficheros de tarea en plan/tasks/ con el formato NNN-slug.md.")
	fmt.Println("2) Ejecuta `forgeworld validate` para verificar las tareas.")
	fmt.Println("3) Ejecuta `forgeworld tui` para iniciar el bucle.")
	if _, err := plan.LoadTasks(root); errors.Is(err, plan.ErrLegacyPlanDetected) {
		fmt.Println("")
		fmt.Println("AVISO: Se detectó plan/plan.yml (formato legacy).")
		fmt.Println("El nuevo formato usa plan/tasks/*.md.")
		fmt.Println("Crea plan/tasks/ y migra tus tareas como ficheros markdown.")
	}
	return nil
}

func parseInitOptions(args []string) (string, bool, error) {
	validatePreset := func(v string) (string, error) {
		if _, err := config.DefaultForExecutorPreset(v); err != nil {
			return "", err
		}
		return v, nil
	}

	usageErr := errors.New("uso: forgeworld init [--executor codex|claude|gemini] [--recreate]")
	preset := ""
	recreate := false
	i := 0
	for i < len(args) {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--recreate":
			recreate = true
			i++
		case strings.HasPrefix(arg, "--executor="):
			if preset != "" {
				return "", false, usageErr
			}
			v := strings.TrimSpace(strings.TrimPrefix(arg, "--executor="))
			if v == "" {
				return "", false, usageErr
			}
			valid, err := validatePreset(v)
			if err != nil {
				return "", false, err
			}
			preset = valid
			i++
		case arg == "--executor" || arg == "-e":
			if preset != "" || i+1 >= len(args) {
				return "", false, usageErr
			}
			v := strings.TrimSpace(args[i+1])
			if v == "" || strings.HasPrefix(v, "-") {
				return "", false, usageErr
			}
			valid, err := validatePreset(v)
			if err != nil {
				return "", false, err
			}
			preset = valid
			i += 2
		case strings.HasPrefix(arg, "-"):
			return "", false, usageErr
		default:
			if preset != "" {
				return "", false, usageErr
			}
			valid, err := validatePreset(arg)
			if err != nil {
				return "", false, err
			}
			preset = valid
			i++
		}
	}
	return preset, recreate, nil
}

func runValidate(root string) error {
	tasks, err := plan.LoadTasks(root)
	if err != nil {
		if errors.Is(err, plan.ErrLegacyPlanDetected) {
			fmt.Println("Se detectó plan/plan.yml (formato legacy). El nuevo formato usa plan/tasks/*.md.")
			fmt.Println("Crea plan/tasks/ y migra tus tareas como ficheros markdown.")
			return nil
		}
		return err
	}
	if len(tasks) == 0 {
		fmt.Println("plan/tasks/ existe pero no contiene ficheros .md")
		return nil
	}
	errs := plan.ValidateTasks(tasks)
	if len(errs) > 0 {
		var sb strings.Builder
		sb.WriteString("validacion fallida:\n")
		for _, e := range errs {
			sb.WriteString("- " + e.Error() + "\n")
		}
		return errors.New(sb.String())
	}
	fmt.Printf("plan/tasks/ valido (%d tareas):\n", len(tasks))
	for _, t := range tasks {
		mark := "[ ]"
		if t.Complete {
			mark = "[x]"
		}
		fmt.Printf("  %s %s (%s)\n", mark, t.Name, t.Model)
	}
	return nil
}

func runTUI(root string) error {
	if err := config.ValidatePromptFiles(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "plan", "tasks")); err != nil {
		return fmt.Errorf("falta plan/tasks/. Ejecuta `forgeworld init` y crea las tareas")
	}
	st, err := engine.LoadState(root)
	if err != nil {
		return err
	}
	errs := plan.ValidateTasks(st.Tasks)
	if len(errs) > 0 {
		return fmt.Errorf("tareas invalidas; ejecuta `forgeworld validate`")
	}
	return ui.Start(context.Background(), st)
}

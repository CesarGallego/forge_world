package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"os"

	"forgeworld/internal/plan"
)

func TestRunOrdenanamientoReportsProgressWhenPlanIsAlreadyValid(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "loop", "tasks", "ordenanamiento"), 0o755); err != nil {
		t.Fatal(err)
	}
	planYAML := `version: 2
phases:
  - type: validation
    name: Preparacion del bucle de forja
    description: desc
    complete: false
    tasks:
      - name: Validar estructura del plan
        description: d1
        complete: false
        model: small
      - name: Crear skills base
        description: d2
        complete: false
        model: small
      - name: Agregar tareas de validacion
        description: d3
        complete: false
        model: medium
`
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.yml"), []byte(planYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	s := &State{Root: root}
	stdout, stderr, code, err := s.runOrdenanamiento(
		context.Background(),
		root,
		"medium",
		filepath.Join(root, "loop", "tasks", "ordenanamiento"),
		func(chunk string) { progress.WriteString(chunk) },
		nil,
	)
	if err != nil {
		t.Fatalf("runOrdenanamiento failed: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected code 0, got %d (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	got := progress.String()
	if !strings.Contains(got, "Validando plan/plan.yml...") {
		t.Fatalf("expected validation progress, got %q", got)
	}
	if !strings.Contains(got, "ya es valido") {
		t.Fatalf("expected already-valid progress message, got %q", got)
	}
}

func TestRunOrdenanamientoIgnoresVersionOnlyValidationError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	planYAML := `version: "1.0"
phases:
  - type: validation
    name: Preparacion del bucle de forja
    description: desc
    complete: false
    tasks:
      - name: Validar estructura del plan
        description: d1
        complete: false
        model: small
      - name: Crear skills base
        description: d2
        complete: false
        model: small
      - name: Agregar tareas de validacion
        description: d3
        complete: false
        model: medium
`
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.yml"), []byte(planYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	s := &State{Root: root}
	stdout, stderr, code, err := s.runOrdenanamiento(
		context.Background(),
		root,
		"medium",
		filepath.Join(root, "loop", "tasks", "ordenanamiento"),
		func(chunk string) { progress.WriteString(chunk) },
		nil,
	)
	if err != nil {
		t.Fatalf("runOrdenanamiento failed: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected code 0, got %d (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "plan/plan.yml valido") {
		t.Fatalf("expected stdout to mark plan valid, got %q", stdout)
	}
	got := progress.String()
	if !strings.Contains(got, "actualizando version del plan") {
		t.Fatalf("expected progress to mention version update, got %q", got)
	}
	updated, _, err := plan.Load(root)
	if err != nil {
		t.Fatalf("failed reading updated plan: %v", err)
	}
	if updated.Version != "2" {
		t.Fatalf("expected plan version to be updated to 2, got %q", updated.Version)
	}
}

package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidateReportsLegacyPlanUpgrade(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	planYAML := `version: 1
phases:
  - name: Fase 1
    description: desc
    complete: false
    tasks:
      - name: T1
        description: d
        complete: false
        model: small
        context: loop/tasks/t1/context.md
`
	if err := os.WriteFile(filepath.Join(root, "plan", "plan.yml"), []byte(planYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runValidate(root); err != nil {
			t.Fatalf("runValidate failed: %v", err)
		}
	})

	if !strings.Contains(out, "requiere actualizacion de version") {
		t.Fatalf("expected upgrade message, got: %s", out)
	}
	if strings.Contains(out, "plan/plan.yml valido") {
		t.Fatalf("did not expect valid message for legacy plan: %s", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

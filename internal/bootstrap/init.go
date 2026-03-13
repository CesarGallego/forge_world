package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"forgeworld"
	"forgeworld/internal/config"
)

const readmeTemplate = `# Forgeworld Plan

Este directorio define el plan operativo del mundo forja.

## Orden de ejecucion

` + "`plan/plan.md`" + ` define el orden de ejecucion y el estado de cada tarea mediante casillas de verificacion:

` + "```markdown" + `
# Plan

- [ ] modelar-entidades
- [ ] crear-api
- [ ] añadir-tests
` + "```" + `

Cada slug referencia un fichero en ` + "`plan/tasks/<slug>.md`" + `. El bucle ejecuta las tareas en el orden declarado en ` + "`plan/plan.md`" + ` y marca cada casilla al completarse.

## Formato de tareas

Las tareas se definen como ficheros markdown en ` + "`plan/tasks/`" + `, uno por tarea.
El nombre del fichero sigue el patron ` + "`NNN-slug.md`" + ` (ej: ` + "`001-modelar-entidades.md`" + `).

### Estructura de un fichero de tarea

` + "```markdown" + `
---
model: small
---
# Nombre de la tarea

Descripcion detallada en markdown libre.
Todo el contexto relevante aqui.
` + "```" + `

### Campos del frontmatter

- ` + "`model`" + ` (obligatorio): ` + "`small`" + `, ` + "`medium`" + ` o ` + "`large`" + `.

### Contexto global

Si existe ` + "`plan/context.md`" + `, su contenido se incluye como contexto global en todas las sesiones.

## Ejemplo completo

` + "```markdown" + `
---
model: small
---
# Modelar entidades

Define las entidades principales del dominio usando DDD.

## Entidades

- Usuario: id, nombre, email
- Pedido: id, usuario_id, items, estado

## Criterios de exito

- Fichero ` + "`domain/entities.go`" + ` con structs y validaciones.
- Tests unitarios en ` + "`domain/entities_test.go`" + `.
` + "```" + `

`

const planPromptTemplate = `# Prompt para crear tareas en plan/tasks/

Actúa como planificador del proyecto usando Forgeworld.

## Reglas obligatorias

1. Lee primero ` + "`plan/README.md`" + ` y sigue su formato al pie de la letra.
2. Crea ficheros en ` + "`plan/tasks/`" + ` con el formato ` + "`NNN-slug.md`" + ` (ej: ` + "`001-modelar-entidades.md`" + `).
3. Cada fichero tiene frontmatter YAML con ` + "`model`" + ` y un H1 como nombre de tarea.
4. Actualiza ` + "`plan/plan.md`" + ` añadiendo una linea ` + "`- [ ] slug`" + ` por cada tarea en el orden de ejecucion deseado.
5. Antes de crear las tareas, pregunta al usuario:
   - qué vamos a construir ahora
   - alcance deseado (MVP vs completo)
   - restricciones técnicas o de tiempo
6. Mantén tareas pequeñas y verificables.
7. No crees fases ni estructuras anidadas; solo ficheros planos en ` + "`plan/tasks/`" + `.

## Formato de cada fichero de tarea

` + "```markdown" + `
---
model: small
---
# Nombre de la tarea

Descripcion y contexto completo aqui.
` + "```" + `

## Formato de plan/plan.md

` + "```markdown" + `
# Plan

- [ ] primer-slug
- [ ] segundo-slug
- [ ] tercer-slug
` + "```" + `

## Salida esperada

- Primero: resumen breve de lo entendido del usuario.
- Segundo: lista de tareas propuestas con nombre, modelo y orden.
- Tercero: contenido de cada fichero ` + "`plan/tasks/NNN-slug.md`" + ` y el ` + "`plan/plan.md`" + ` actualizado, listos para guardar.
`

func EnsureLayout(root, executorPreset string) ([]string, error) {
	created := []string{}
	for _, rel := range []string{"plan", "plan/tasks", "loop", "loop/runs", "loop/skills", "loop/roles"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return created, err
		}
	}

	// Create plan/plan.md on fresh init (when plan/tasks/ is empty).
	planMd := filepath.Join(root, "plan", "plan.md")
	if _, statErr := os.Stat(planMd); os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(filepath.Join(root, "plan", "tasks"))
		hasTasks := false
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				hasTasks = true
				break
			}
		}
		if !hasTasks {
			if err := os.WriteFile(planMd, []byte("# Plan\n\n"), 0o644); err != nil {
				return created, err
			}
			created = append(created, planMd)
		}
	}

	readme := filepath.Join(root, "plan", "README.md")
	_, statErr := os.Stat(readme)
	if statErr != nil && !os.IsNotExist(statErr) {
		return created, statErr
	}
	if err := os.WriteFile(readme, []byte(readmeTemplate), 0o644); err != nil {
		return created, err
	}
	if os.IsNotExist(statErr) {
		created = append(created, readme)
	}
	planPrompt := filepath.Join(root, "plan", "prompt.md")
	_, promptStatErr := os.Stat(planPrompt)
	if promptStatErr != nil && !os.IsNotExist(promptStatErr) {
		return created, promptStatErr
	}
	if err := os.WriteFile(planPrompt, []byte(planPromptTemplate), 0o644); err != nil {
		return created, err
	}
	if os.IsNotExist(promptStatErr) {
		created = append(created, planPrompt)
	}
	if ok, err := config.SaveDefaultIfMissing(root, executorPreset); err != nil {
		return created, err
	} else if ok {
		created = append(created, filepath.Join(root, ".forgeworld.yml"))
	}
	return created, nil
}

func EnsurePromptDirHint() (string, error) {
	dir, err := config.PromptDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return fmt.Sprintf("Prompts en %s (%s). `forgeworld init --recreate` los sobrescribe con las plantillas de templates/prompts/.", dir, strings.Join([]string{"alpha.md", "error.md", "review.md", "judge.md", "merge.md", "done.md", "plan.md", "crit-error.md"}, ", ")), nil
}

func EnsurePromptFiles(recreate bool) ([]string, error) {
	dir, err := config.PromptDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	templates := map[string]string{
		"alpha.md":      "templates/prompts/alpha.md",
		"error.md":      "templates/prompts/error.md",
		"review.md":     "templates/prompts/review.md",
		"judge.md":      "templates/prompts/judge.md",
		"merge.md":      "templates/prompts/merge.md",
		"done.md":       "templates/prompts/done.md",
		"plan.md":       "templates/prompts/plan.md",
		"crit-error.md": "templates/prompts/crit-error.md",
	}
	names := make([]string, 0, len(templates))
	for name := range templates {
		names = append(names, name)
	}
	sort.Strings(names)

	written := []string{}
	for _, name := range names {
		src := templates[name]
		dst := filepath.Join(dir, name)
		if !recreate {
			if _, err := os.Stat(dst); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return written, err
			}
		}
		body, err := forgeworld.TemplateFS.ReadFile(src)
		if err != nil {
			return written, err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return written, err
		}
		written = append(written, dst)
	}
	return written, nil
}

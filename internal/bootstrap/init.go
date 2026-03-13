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

## Formato de tareas

Las tareas se definen como ficheros markdown en ` + "`plan/tasks/`" + `, uno por tarea.

El nombre del fichero determina el orden de ejecucion: ` + "`001-slug.md`" + `, ` + "`002-slug.md`" + `, etc.

### Estructura de un fichero de tarea

` + "```markdown" + `
---
model: small
complete: false
---
# Nombre de la tarea

Descripcion detallada en markdown libre.
Todo el contexto relevante aqui.
` + "```" + `

### Campos del frontmatter

- ` + "`model`" + ` (obligatorio): ` + "`small`" + `, ` + "`medium`" + ` o ` + "`large`" + `.
- ` + "`complete`" + ` (obligatorio, boolean): ` + "`false`" + ` hasta que la tarea sea completada por el bucle.

### Contexto global

Si existe ` + "`plan/context.md`" + `, su contenido se incluye como contexto global en todas las sesiones.

## Ejemplo completo

` + "```markdown" + `
---
model: small
complete: false
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
3. Cada fichero tiene frontmatter YAML con ` + "`model`" + ` y ` + "`complete: false`" + `, y un H1 como nombre de tarea.
4. Antes de crear las tareas, pregunta al usuario:
   - qué vamos a construir ahora
   - alcance deseado (MVP vs completo)
   - restricciones técnicas o de tiempo
5. Mantén tareas pequeñas y verificables.
6. No crees fases ni estructuras anidadas; solo ficheros planos en ` + "`plan/tasks/`" + `.

## Formato de cada fichero

` + "```markdown" + `
---
model: small
complete: false
---
# Nombre de la tarea

Descripcion y contexto completo aqui.
` + "```" + `

## Salida esperada

- Primero: resumen breve de lo entendido del usuario.
- Segundo: lista de tareas propuestas con nombre y modelo.
- Tercero: contenido de cada fichero ` + "`plan/tasks/NNN-slug.md`" + ` listo para guardar.
`

func EnsureLayout(root, executorPreset string) ([]string, error) {
	created := []string{}
	for _, rel := range []string{"plan", "plan/tasks", "loop", "loop/runs", "loop/skills"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return created, err
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
	return fmt.Sprintf("Prompts en %s (%s). `forgeworld init --recreate` los sobrescribe con las plantillas de templates/prompts/.", dir, strings.Join([]string{"alpha.md", "error.md", "review.md"}, ", ")), nil
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
		"alpha.md":  "templates/prompts/alpha.md",
		"error.md":  "templates/prompts/error.md",
		"review.md": "templates/prompts/review.md",
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

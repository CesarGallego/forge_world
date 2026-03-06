package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"forgeworld/internal/config"
)

const readmeTemplate = `# Forgeworld Plan

Este directorio define el plan operativo del mundo forja.

## Reglas

- El archivo de control es ` + "`plan/plan.yml`" + `.
- Las descripciones de fases y tareas no tienen limite de longitud impuesto por forgeworld.
- Cada tarea debe declarar ` + "`model: small|medium|large`" + `.
- El contexto debe ser minimo: divide tareas para consumir poco contexto.
- Puede haber nodos paralelos con exactamente 2 tareas usando ` + "`parallel`" + `.
- Las LLM no modifican el plan; solo lo hace el binario de forma determinista.

## Estructura de ` + "`plan.yml`" + `

` + "`plan.yml`" + ` acepta este esquema:

- ` + "`context`" + ` (opcional): contexto global del plan.
- ` + "`phases`" + ` (obligatorio): lista de fases.

Cada fase:

- ` + "`type`" + ` (opcional):
  - omitido o ` + "`user`" + `: fase definida por el usuario.
  - ` + "`validation`" + `: fase interna de forgeworld.
- ` + "`name`" + ` (obligatorio, no vacio).
- ` + "`description`" + ` (obligatorio).
- ` + "`complete`" + ` (obligatorio, boolean).
- ` + "`context`" + ` (opcional).
- ` + "`tasks`" + ` (obligatorio): lista de nodos.

Importante:

- Al crear tu plan, usa fases de usuario (` + "`type`" + ` omitido o ` + "`user`" + `).
- **No generes una fase 0 ni una fase de preparacion.** Las fases deben ser directamente de implementacion.
- ` + "`type: validation`" + ` es reservado para forgeworld (fase interna del proceso).
- Debe existir **un fichero de contexto por tarea**.
- El ` + "`context`" + ` de cada tarea debe apuntar a ` + "`loop/tasks/<slug-tarea>/context.md`" + `.

Cada nodo en ` + "`tasks`" + ` puede ser uno de estos dos formatos:

1. Tarea simple:

` + "```yaml" + `
- name: Definir contratos de dominio
  description: Define entidades y reglas base.
  complete: false
  model: small
  context: loop/tasks/definir-contratos-de-dominio/context.md
` + "```" + `

2. Nodo paralelo (exactamente 2 tareas):

` + "```yaml" + `
- parallel:
    - name: Implementar repositorio
      description: Crea adaptador SQL para entidades.
      complete: false
      model: medium
      context: loop/tasks/implementar-repositorio/context.md
    - name: Implementar tests de repositorio
      description: Cubre casos base y errores.
      complete: false
      model: medium
      context: loop/tasks/implementar-tests-de-repositorio/context.md
` + "```" + `

Campos de cada tarea:

- ` + "`name`" + ` (obligatorio, no vacio).
- ` + "`description`" + ` (obligatorio).
- ` + "`complete`" + ` (obligatorio, boolean).
- ` + "`model`" + ` (obligatorio): ` + "`small`" + `, ` + "`medium`" + ` o ` + "`large`" + `.
- ` + "`context`" + ` (obligatorio): ` + "`loop/tasks/<slug-tarea>/context.md`" + `.
- ` + "`state`" + ` (opcional): estado runtime, lo gestiona forgeworld.

Ejemplo completo minimo (fases de usuario):

` + "```yaml" + `
context: "Objetivo: entregar MVP funcional"
phases:
  - name: Fase 1 - Backend base
    description: Implementa base de dominio y persistencia inicial.
    complete: false
    tasks:
      - name: Modelar entidades
        description: Define entidades y reglas de negocio iniciales.
        complete: false
        model: small
        context: loop/tasks/modelar-entidades/context.md
      - parallel:
          - name: Persistencia SQL
            description: Implementa repositorios SQL para entidades principales.
            complete: false
            model: medium
            context: loop/tasks/persistencia-sql/context.md
          - name: Tests persistencia
            description: Crea tests de persistencia para casos base y error.
            complete: false
            model: medium
            context: loop/tasks/tests-persistencia/context.md
` + "```" + `

## Validar el plan con el cliente de forgeworld

Desde la raiz del proyecto:

` + "```bash" + `
forgeworld validate
` + "```" + `

Durante esta validacion, forgeworld ajusta internamente la fase ` + "`type: validation`" + ` (la inserta o reordena si hace falta).

Si el plan es valido, veras:

` + "```text" + `
plan/plan.yml valido
` + "```" + `

Si hay errores, el cliente devuelve ` + "`validacion fallida`" + ` con detalle por ruta YAML (por ejemplo ` + "`phase[1].tasks[0].context`" + `).

Nota sobre contexto de tareas:

- El bucle alpha puede crear el fichero de contexto si no existe.
- El bucle alpha puede editar el contenido del fichero de contexto.
- El bucle alpha no debe desviar ` + "`context`" + ` a un fichero de otra tarea.

Nota sobre ` + "`type: validation`" + `:

- Forgeworld usa ese tipo para la fase interna de validacion.
- No dependas del nombre de la fase; la identificacion se hace por ` + "`type`" + `.

## Skills

Las skills viven en ` + "`loop/skills/`" + `. Son arboles de ficheros con metodologia.
En alpha solo se referencia el fichero raiz relevante; la creacion de skills es tarea de fase 0.

## Prompt globales obligatorios

Debes crear en ` + "`~/.config/forgeworld/`" + `:

- ` + "`alpha.md`" + `
- ` + "`error.md`" + `
- ` + "`phase0.md`" + `
- ` + "`ordenanamiento.md`" + `

Puedes partir de las plantillas del proyecto en ` + "`templates/prompts/`" + `.
`

const planPromptTemplate = `# Prompt para crear/actualizar plan/plan.yml

Actúa como planificador del proyecto usando Forgeworld.

## Reglas obligatorias

1. Lee primero ` + "`plan/README.md`" + ` y sigue su formato al pie de la letra.
2. Si ya existe ` + "`plan/plan.yml`" + `:
   - revisa si **todas** las fases están terminadas (` + "`complete: true`" + ` en fase y tareas).
   - solo en ese caso puedes reemplazarlo por un plan nuevo.
   - si no está totalmente terminado, no lo borres: propón actualización incremental.
3. Antes de generar o modificar el plan, pregunta al usuario:
   - qué vamos a construir ahora
   - alcance deseado (MVP vs completo)
   - restricciones técnicas o de tiempo
4. Mantén tareas pequeñas y verificables.
5. No inventes estructura fuera de lo definido en ` + "`plan/README.md`" + `.

## Salida esperada

- Primero: resumen breve de lo entendido del usuario.
- Segundo: propuesta de plan.
- Tercero: ` + "```yaml" + `
  contenido final de ` + "`plan/plan.yml`" + ` listo para guardar.
  ` + "```" + `
`

func EnsureLayout(root, executorPreset string) ([]string, error) {
	created := []string{}
	for _, rel := range []string{"plan", "loop", "loop/tasks", "loop/runs", "loop/skills"} {
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
	return fmt.Sprintf("Prompts en %s (%s). `forgeworld init --recreate` los sobrescribe con las plantillas de templates/prompts/.", dir, strings.Join([]string{"alpha.md", "error.md", "phase0.md", "ordenanamiento.md"}, ", ")), nil
}

func EnsurePromptFiles(root string, recreate bool) ([]string, error) {
	dir, err := config.PromptDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	templates := map[string]string{
		"alpha.md":          filepath.Join(root, "templates", "prompts", "alpha.md"),
		"error.md":          filepath.Join(root, "templates", "prompts", "error.md"),
		"phase0.md":         filepath.Join(root, "templates", "prompts", "phase0.md"),
		"ordenanamiento.md": filepath.Join(root, "templates", "prompts", "ordenanamiento.md"),
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
		body, err := os.ReadFile(src)
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

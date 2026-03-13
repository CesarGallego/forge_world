# Forgeworld

## Objetivo (visión general)

Forgeworld es un bucle de ejecución tipo Ralph para construir software con agentes LLM de forma controlada:

1. Tomas un objetivo de producto.
2. Lo conviertes en un plan de tareas (`plan/tasks/*.md`).
3. Ejecutas el bucle con TUI para que el agente vaya resolviendo tareas.
4. Validas el plan cuando haga falta (`validate`).

La idea es iterar rápido, con trazabilidad de lo que hizo el agente y control humano sobre el plan.

## Flujo recomendado

### 1) Inicializar proyecto

```bash
forgeworld init --executor codex
```

También puedes usar `claude` o `gemini`.

Esto prepara estructura base (`plan/`, `loop/`, etc.) y prompts globales.

Si quieres reinstalar y sobrescribir prompts básicos:

```bash
forgeworld init --recreate
```

Si no pasas `--recreate`, **no** se sobreescriben prompts existentes.

### 2) Crear el plan con tu agente favorito

Abre tu agente favorito (Codex/Claude/Gemini) y ordénale ejecutar `plan/prompt.md`.

Ese prompt ya incluye:
- leer `plan/README.md` antes de planificar,
- preguntar al usuario qué se va a construir,
- decidir si corresponde reemplazar plan previo o actualizar incrementalmente.

El resultado esperado son ficheros en `plan/tasks/` con el formato `NNN-slug.md`.

### 3) Validar el plan

```bash
forgeworld validate
```

### 4) Ejecutar el bucle

Con TUI interactiva (inspección de tareas y logs en vivo):

```bash
forgeworld tui
```

O en modo headless para CI/CD o ejecución desatendida:

```bash
forgeworld run
```

## Comandos principales

- `forgeworld init [--executor codex|claude|gemini] [--recreate]`
- `forgeworld validate`
- `forgeworld tui`
- `forgeworld run`
- `forgeworld help`
- `forgeworld help <init|validate|tui|run>`

## Prompts (edición manual)

Forgeworld usa prompts globales en:

- `~/.config/forgeworld/alpha.md`
- `~/.config/forgeworld/error.md`
- `~/.config/forgeworld/review.md`

Plantillas del repositorio:

- `templates/prompts/`

Importante:
- Puedes editar estos prompts a mano.
- `forgeworld init` solo crea los que faltan.
- `forgeworld init --recreate` los sobrescribe con plantillas base.
- `forgeworld init --recreate` también puede restaurar el set base si los tocaste y quieres reiniciar.

## Atajos TUI (resumen)

- `q` salir (`Q` forzar)
- `left/right` cambiar `stdout/stderr`
- `j/k` cambiar tarea inspeccionada
- `u/d` scroll log
- `g/G` inicio/final log

## Instalación local

```bash
just install
```

Instala binario y plantillas de prompts.

# Forgeworld

## Objetivo (visión general)

Forgeworld es un bucle de ejecución tipo Ralph para construir software con agentes LLM de forma controlada:

1. Tomas un objetivo de producto.
2. Lo conviertes en un plan de tareas (`plan/plan.yml`).
3. Ejecutas el bucle con TUI para que el agente vaya resolviendo tareas.
4. Validas y corriges el plan cuando haga falta (`validate` / `fix`).

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

Usa `plan/README.md` como contrato de formato y pídele a tu agente favorito (Codex/Claude/Gemini) que te genere `plan/plan.yml` siguiendo esas reglas.

### 3) Validar el plan

```bash
forgeworld validate
```

### 4) Ejecutar el bucle en TUI

```bash
forgeworld tui
```

La TUI te deja inspeccionar árbol de tareas y logs en vivo.

### 5) Corregir plan si se rompe

```bash
forgeworld fix
```

`fix` ejecuta la fase de ordenanamiento para intentar dejar `plan/plan.yml` válido.

## Comandos principales

- `forgeworld init [--executor codex|claude|gemini] [--recreate]`
- `forgeworld validate`
- `forgeworld fix`
- `forgeworld tui`
- `forgeworld help`
- `forgeworld help <init|validate|fix|tui>`

## Prompts (edición manual)

Forgeworld usa prompts globales en:

- `~/.config/forgeworld/alpha.md`
- `~/.config/forgeworld/error.md`
- `~/.config/forgeworld/phase0.md`
- `~/.config/forgeworld/ordenanamiento.md`

Plantillas del repositorio:

- `templates/prompts/`

Importante:
- Puedes editar estos prompts a mano.
- `forgeworld init` solo crea los que faltan.
- `forgeworld init --recreate` los sobrescribe con plantillas base.

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


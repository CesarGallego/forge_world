# Prueba de aceptacion: runtime de sesiones, reviewer y squash merge

## Objetivo

Verificar que el runtime actual de Forgeworld cumple estos puntos:

- `forgeworld validate` distingue entre plan valido y plan legacy.
- Un plan legacy (`version: 1`) no falla, pero anuncia upgrade obligatorio.
- El estado runtime se persiste en `loop/runtime/state.yml`, incluso si una sesion falla o es rechazada.
- Las sesiones normales usan `loop/sessions/` y `loop/worktrees/` en repos git.
- Toda integracion aprobada pasa por `REVIEW` antes de `MERGE`.
- Los logs persistidos incluyen `=== MERGE ===` cuando aplica.
- El merge final aprobado es por squash.
- Un worktree aprobado se limpia; uno rechazado o fallido se conserva.

## Precondiciones

1. Estar en un repo git que puedas ensuciar o usar un repo temporal.
2. Tener `forgeworld` compilado desde este repo.
3. Tener un executor funcional en `.forgeworld.yml`.
4. Tener prompts globales instalados con:

```bash
forgeworld init --recreate
```

## Fixture base

Usar este `plan/plan.yml`:

```yaml
version: 2
context: "Prueba de runtime"
phases:
  - name: Fase 1
    description: Validar flujo basico
    complete: false
    tasks:
      - name: Crear archivo de prueba
        description: Crear un archivo simple para comprobar la sesion
        complete: false
        model: small
        context: loop/tasks/crear-archivo-de-prueba/context.md
```

Crear el contexto:

```bash
mkdir -p loop/tasks/crear-archivo-de-prueba
printf '%s\n' 'Crear test-output.txt con contenido "ok".' > loop/tasks/crear-archivo-de-prueba/context.md
```

## Caso 1: validacion de plan versionado

### Ejecucion

```bash
forgeworld validate
```

### Resultado esperado

- El comando termina en exito.
- La salida incluye `plan/plan.yml valido`.
- No aparece mensaje de upgrade.

## Caso 2: plan legacy anuncia upgrade sin fallar

### Preparacion

Cambiar la primera linea del plan a:

```yaml
version: 1
```

### Ejecucion

```bash
forgeworld validate
```

### Resultado esperado

- El comando termina en exito.
- La salida incluye `plan/plan.yml requiere actualizacion de version`.
- La salida no incluye `plan/plan.yml valido`.

## Caso 3: upgrade runtime crea sesion dedicada

### Ejecucion

Lanzar `forgeworld tui` con el plan legacy y dejar que procese la sesion de upgrade.

### Resultado esperado

- La primera sesion creada es `plan-upgrade-session`.
- Se crea `loop/runtime/state.yml`.
- Se crea `loop/sessions/plan-upgrade-session/`.
- Si el upgrade se aprueba y se integra, el plan queda en `version: 2`.

### Verificaciones en disco

```bash
test -f loop/runtime/state.yml
test -d loop/sessions/plan-upgrade-session
sed -n '1,80p' loop/runtime/state.yml
sed -n '1,20p' plan/plan.yml
```

Comprobar:

- `loop/runtime/state.yml` existe desde la primera iteracion.
- Durante el mismatch inicial aparece `upgrade_needed: true`.
- Tras completar el upgrade, `plan_version: "2"` y `version: "2"` en runtime.
- `plan/plan.yml` queda con `version: 2`.

## Caso 4: sesion normal usa runtime separado y worktree

### Preparacion

Restaurar el plan a `version: 2`.

Si quieres aislar este caso, deja la fase interna de validacion ya completada o usa un repo de prueba donde no importe que Forgeworld inserte `phase0`.

### Ejecucion

Lanzar `forgeworld tui` y dejar que procese la sesion de la tarea `Crear archivo de prueba`.

### Resultado esperado

- Se crea `loop/sessions/<session-id>/`.
- Se crea `loop/worktrees/<session-id>/` mientras la sesion este viva.
- El estado fino de la sesion no se escribe en `plan/plan.yml`.
- El estado operativo queda en `loop/runtime/state.yml`.

### Verificaciones

```bash
find loop/sessions -maxdepth 2 \( -name prompt.md -o -name omega.md -o -name review.md -o -name context.yml \)
find loop/worktrees -maxdepth 2 -type d
sed -n '1,220p' loop/runtime/state.yml
sed -n '1,120p' plan/plan.yml
```

Comprobar en `loop/runtime/state.yml`:

- existe una sesion con `status`
- la sesion tiene `branch`
- la sesion tiene `worktree_path`
- la sesion tiene `session_dir`

Comprobar en `plan/plan.yml`:

- existe `version: 2`
- no aparecen `branch`, `worktree_path`, `review_verdict` ni otros campos runtime

## Caso 5: review obligatorio y logs persistidos completos

### Ejecucion

Procesar una sesion aprobada completa.

### Resultado esperado

- El log incluye `=== ALPHA ===`.
- El log incluye `=== OMEGA ===`.
- El log incluye `=== REVIEW ===`.
- El log incluye `=== MERGE ===`.
- La sesion no pasa a `merged` sin review aprobado.

### Verificaciones

```bash
LATEST_STDOUT=$(find loop/runs -maxdepth 2 -name stdout.log | tail -n 1)
sed -n '1,240p' "$LATEST_STDOUT"
sed -n '1,220p' loop/runtime/state.yml
```

Comprobar:

- `stdout.log` contiene `=== REVIEW ===` antes de `=== MERGE ===`
- `stdout.log` contiene el mensaje de commit `forgeworld(merge): ...`
- `loop/runtime/state.yml` guarda `review_verdict: approved`
- `loop/runtime/state.yml` guarda `status: merged`

## Caso 6: merge por squash y limpieza de worktree aprobado

### Ejecucion

Completar una sesion aprobada en un repo git.

### Resultado esperado

- La rama principal recibe un commit nuevo `forgeworld(merge): ...`.
- La rama `forgeworld/<session-id>` se elimina.
- El worktree aprobado se elimina.

### Verificaciones git

```bash
git log --oneline -n 5
git branch --list 'forgeworld/*'
git worktree list
```

Comprobar:

- hay un commit reciente `forgeworld(merge): ...`
- `git branch --list 'forgeworld/*'` no devuelve ramas de sesion aprobadas
- `git worktree list` no muestra el worktree aprobado

## Caso 7: rechazo o fallo persiste estado y conserva worktree

### Preparacion

Forzar que el reviewer responda `REJECTED` o que omega falle.

### Ejecucion

Procesar la sesion y dejar que termine en error o rechazo.

### Resultado esperado

- La TUI termina la iteracion con error.
- `loop/runtime/state.yml` sigue guardando el estado final de la sesion.
- El worktree no se elimina.

### Verificaciones

```bash
sed -n '1,240p' loop/runtime/state.yml
git branch --list 'forgeworld/*'
git worktree list
find loop/worktrees -maxdepth 2 -type d
```

Comprobar:

- la sesion queda con `status: rejected` o `status: failed`
- aparece `review_verdict: rejected` cuando el reviewer rechaza
- `worktree_path` sigue presente en runtime
- el worktree sigue existiendo en disco y en `git worktree list`

## Criterio de aceptacion final

La implementacion se considera correcta si:

1. `forgeworld validate` informa upgrade para `version: 1` sin marcar el plan como valido.
2. El mismatch activa una sesion especial `plan-upgrade-session`.
3. El runtime persistente vive en `loop/runtime` y `loop/sessions`, no en el plan.
4. Las sesiones git usan worktrees aislados cuando git esta disponible.
5. Toda integracion aprobada pasa por review obligatorio antes del merge.
6. Los logs persistidos incluyen `ALPHA`, `OMEGA`, `REVIEW` y `MERGE` cuando aplica.
7. La integracion aprobada se materializa como squash merge.
8. Los worktrees aprobados se limpian.
9. Los worktrees fallidos o rechazados se conservan y su estado queda persistido en runtime.

## Automatizacion recomendada

Si esta prueba se automatiza, conviene separar:

- test CLI para `validate`
- test integration para `LoopOnce` con git/worktree
- test acceptance para reviewer, runtime persistido y squash merge

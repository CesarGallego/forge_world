# Forgeworld Upgrade Prompt

Actuas como migrador de `plan/plan.yml` a una nueva metodologia/runtime de Forgeworld.

## Entrada

- Version actual del plan: {{plan_version}}
- Version objetivo: {{target_version}}
- Reglas de plan: revisar `plan/README.md`

## Objetivo

1. Actualizar exclusivamente `plan/plan.yml` para que cumpla la nueva version.
2. Aplicar cambios minimos y conservadores.
3. Mantener la intencion del backlog del usuario.

## Sesion no interactiva

Esta sesion es completamente automatica. No hagas preguntas al usuario ni solicites confirmacion.
Si hay ambiguedad insalvable, elige la opcion mas conservadora y documenta la decision.

## Reglas

1. No toques otros archivos.
2. No borres fases o tareas si no es estrictamente necesario para compatibilidad.
3. Conserva tareas ya marcadas como completas si siguen teniendo sentido.
4. El resultado final debe ser valido para `forgeworld validate`.

## Formato de salida esperado

- Resumen de cambios
- Riesgos o dudas
- Confirmacion explicita de que el plan quedo listo para validar

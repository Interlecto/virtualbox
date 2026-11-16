# TODO

Pendientes inmediatos y decisiones abiertas. Los pasos grandes están en [ROADMAP.md](ROADMAP.md). Cada elemento se trabaja en su propia rama con PR hacia `master` (ver [CLAUDE.md](CLAUDE.md)); cuando exista, se enlaza el issue.

## Inmediato
- [ ] Activar protección de la rama `master` en GitHub (PR obligatorio, CI verde)
- [ ] Crear issues en GitHub para cada paso de la Fase 1

## Decisiones abiertas
- [ ] Librería SMTP: `emersion/go-smtp` (recomendada) frente a implementación propia
- [ ] Almacén de configuración: SQLite al inicio, PostgreSQL para producción multi-nodo
- [ ] Serialización del campo `Action` en `queue.Envelope` (hoy es `[]byte` opaco)
- [ ] Modelo de usuarios y credenciales de submission (propio frente a externo)
- [ ] Evaluar Stalwart y Maddy como referencia o base antes de la Fase 1
- [ ] Estadísticas (1.10): confirmar cubetas (hora/día), retención por defecto y si el volcado usa la misma base SQLite que la configuración
- [ ] Estadísticas: lista cerrada de dimensiones permitidas para evitar explosión de cardinalidad
- [ ] Revisar el wishlist del ROADMAP y proponer qué ideas suben al flujo principal tras la Fase 3

## Deuda técnica
- [ ] Cola (1.1): medir rendimiento (hoy un fsync por entrada) y valorar agrupar escrituras
- [ ] Cola (1.1): añadir *jitter* al backoff y límite de espacio en disco para la cola
- [ ] Cola (1.1): contador `Depth()` listo para estadísticas; conectarlo en el paso 1.10
- [ ] Despachador (1.4): usar `queue.RetryPolicy` y `dsn.Build` al fallar o expirar una entrega (DSN con remitente nulo, nunca para remitente nulo)
- [ ] Pruebas de integración con un servidor SMTP real (Docker)
- [ ] Linter (`golangci-lint`) en CI

## Issues y fixes
_Ninguno todavía._

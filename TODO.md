# TODO

Pendientes inmediatos y decisiones abiertas. Los pasos grandes están en [ROADMAP.md](ROADMAP.md). Cada elemento se trabaja en su propia rama con PR hacia `master` (ver [CLAUDE.md](CLAUDE.md)); cuando exista, se enlaza el issue.

## Inmediato
- [ ] Instalar Go en la máquina de desarrollo y verificar que `go build ./...` y `go test ./...` pasan (el esqueleto se escribió sin poder compilar)
- [ ] Activar protección de la rama `master` en GitHub (PR obligatorio, CI verde)
- [ ] Crear issues en GitHub para cada paso de la Fase 1

## Decisiones abiertas
- [ ] Formato de la cola: bbolt, SQLite en modo WAL o ficheros propios (afecta 1.1)
- [ ] Librería SMTP: `emersion/go-smtp` (recomendada) frente a implementación propia
- [ ] Almacén de configuración: SQLite al inicio, PostgreSQL para producción multi-nodo
- [ ] Serialización del campo `Action` en `queue.Envelope` (hoy es `[]byte` opaco)
- [ ] Modelo de usuarios y credenciales de submission (propio frente a externo)
- [ ] Evaluar Stalwart y Maddy como referencia o base antes de la Fase 1

## Deuda técnica
- [ ] Implementación en memoria de `queue.Queue` para pruebas de otros paquetes
- [ ] Pruebas de integración con un servidor SMTP real (Docker)
- [ ] Linter (`golangci-lint`) en CI

## Issues y fixes
_Ninguno todavía._

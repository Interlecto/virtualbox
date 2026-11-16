# CLAUDE.md

Guía para Claude Code (y para cualquier colaborador) en este repositorio.

## Qué es este proyecto

`virtualbox` es un MTA virtual en Go para dominios registrados: recibe, redirige (alias, catch-all), auto-responde, envía (submission + relay) y gestiona listas de correo. **No almacena correo**: lo entrega a backends externos; la única excepción es el buzón temporal (cuota + TTL). Ver [README.md](README.md) para la arquitectura, [ROADMAP.md](ROADMAP.md) para las fases y [TODO.md](TODO.md) para pendientes.

Repositorio: https://github.com/interlecto/virtualbox (público, licencia Apache 2.0). Rama principal: `master`.

## Flujo de trabajo con Git (obligatorio)

- **Nunca se hace commit ni push directo a `master`** (el commit inicial del esqueleto es la única excepción).
- Cada **paso del roadmap, feature, issue o fix** se desarrolla en **su propia rama**, creada desde `master` actualizado, y se integra **mediante Pull Request hacia `master`**.
- Una rama = un propósito. No mezclar pasos ni arreglos no relacionados.
- Nombres de rama:
  - `roadmap/<fase>-<paso>-<slug>` — p. ej. `roadmap/1.1-persistent-queue`
  - `feature/<slug>` — p. ej. `feature/catch-all-per-domain`
  - `issue/<n>-<slug>` — p. ej. `issue/12-dsn-format`
  - `fix/<slug>` — p. ej. `fix/srs-address-length`
  - `docs/<slug>`, `chore/<slug>`
- Mensajes de commit en estilo *Conventional Commits* (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`).
- El PR debe: describir el cambio, referenciar el paso del roadmap o el issue (`Closes #N`), pasar CI y actualizar `ROADMAP.md`/`TODO.md`/documentación cuando corresponda (marcar el paso como hecho en el mismo PR).
- Merge preferido: *squash and merge*. Borrar la rama tras el merge.

## Convenciones de código

- Go ≥ 1.22. Código, identificadores y comentarios en **inglés**; documentación (`*.md`) en **español**.
- Código bajo `internal/`; el binario en `cmd/virtualbox`. Preferir la biblioteca estándar; añadir dependencias solo con justificación (anotarla en el PR).
- Antes de abrir un PR: `gofmt -l .` (sin salida), `go vet ./...`, `go test ./...`.
- Las interfaces de `queue`, `policy` y `backend` son los puntos de extensión: cambiarlas requiere mencionarlo explícitamente en el PR.
- Errores: envolver con `%w`; los fallos de entrega distinguen temporal (reintento) de permanente (`backend.PermanentError`).

## Reglas de diseño que no se deben romper

1. **Durabilidad**: no responder `250 OK` hasta que el mensaje esté persistido (fsync) en la cola.
2. **No accept-then-bounce**: rechazar con 5xx durante la sesión SMTP lo que no se pueda entregar (dominio o destinatario desconocido); evita *backscatter*.
3. **Sin open relay**: submission siempre autenticada y con remitente autorizado para la credencial. El puerto 25 solo acepta destinatarios de dominios registrados.
4. **Reenvíos con SRS** y firma DKIM del dominio; nunca reenviar conservando el sobre original.
5. **Autoresponders** conforme al RFC 3834: sin respuesta a remitente vacío, listas, masivos ni `Auto-Submitted`; límite por remitente.
6. **Sin almacenamiento local de correo** salvo `internal/tempbox`, siempre con cuota y TTL.
7. **Secretos** (claves DKIM, TLS, credenciales) nunca en el repositorio ni en logs.
8. **Estadísticas**: toda función nueva registra sus contadores en `internal/stats` (ver [docs/ESTADISTICAS.md](docs/ESTADISTICAS.md)). Solo agregados; nunca contenido de mensajes ni direcciones de terceros.

## Roadmap y wishlist

- `ROADMAP.md` tiene el flujo principal (fases y pasos numerados) y, al final, el **wishlist/backlog de ideas**.
- Las ideas nuevas se añaden al wishlist. Para subir una al flujo principal se hace un PR `docs/…` que le asigne fase y paso y marque la idea con `→ N.N`.
- Al completar un paso, su PR cambia ⬜ por ✅ en `ROADMAP.md`.

## Entorno

- Desarrollo en Windows 11 (PowerShell/Git Bash). Para probar puertos reales y entrega usar Docker o WSL; producción es Linux con IP fija, PTR coherente y puerto 25 abierto.
- Configuración de ejemplo: `config.example.json` (copiar a `config.json`, ignorado por git).
- CI: `.github/workflows/ci.yml` (gofmt, vet, test, build) en cada PR.

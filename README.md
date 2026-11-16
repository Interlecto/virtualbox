# virtualbox

Buzón virtual de correo electrónico: un servidor de correo (MTA) para dominios registrados que **recibe, redirige, responde y envía, pero no guarda correo**. El almacenamiento se delega a servidores externos (IMAP, LMTP, webhook, S3, archivo).

> **Estado:** esqueleto inicial. Aún no hay servicios SMTP funcionando. Vea [ROADMAP.md](ROADMAP.md) y [TODO.md](TODO.md).

## Características objetivo

1. **Recepción** para dominios registrados:
   - redirecciones a una o más direcciones,
   - redirección *catch-all*,
   - auto-responders,
   - buzón temporal (único almacenamiento local, con cuota y TTL).
2. **Envío** desde dominios registrados:
   - servidor SMTP de envío (submission) autenticado, con TLS y firma DKIM,
   - *relay*: reenvía un correo como si partiera de un buzón del dominio.
3. **Listas de correo** de difusión y de discusión.
4. **Almacenamiento externo** conectable como buzón o archivo.
5. **Registrable en DNS** como servidor de correo (MX), con asistente para SPF, DKIM, DMARC, MTA-STS y TLSRPT.
6. **Estadísticas básicas de uso** integradas (qué opciones se usan y cuánto), consultables por CLI y API sin servicio externo. Ver [docs/ESTADISTICAS.md](docs/ESTADISTICAS.md).

Las ideas futuras están en el [wishlist del roadmap](ROADMAP.md#wishlist-y-backlog-de-ideas).

## Arquitectura

Monolito modular en **Go**, un solo binario.

```
 Internet ──► :25  Listener MX ─┐
 Clientes ──► :587/465 Submission ┤──► Motor de políticas ──► COLA PERSISTENTE ──► Despachador
                 (auth)          │    (SPF/DKIM/DMARC,        (disco, reintentos)      │
                                 │     antispam, límites,          ┌───────────────────┼──────────────────┐
                                 │     resolución de destino)      ▼                   ▼                  ▼
                                 │                          Entrega saliente    Backends externos    Buzón temporal
                                 │                          (MX, TLS, DKIM,     (LMTP, IMAP, webhook, (cuota + TTL)
                                 │                           SRS, MTA-STS)       S3, SMTP)
                                 └──► Motor de listas · Autoresponder (generan mensajes nuevos a la cola)
```

Principio clave: tras responder `250 OK` el mensaje **no puede perderse**. La cola persiste a disco (con fsync) y reintenta hasta entregar o generar un DSN. "No almacena" significa "solo almacena en tránsito".

### Estructura del código

| Ruta | Contenido |
|---|---|
| `cmd/virtualbox` | Punto de entrada del demonio |
| `internal/config` | Configuración estática de arranque |
| `internal/queue` | Interfaz de la cola persistente |
| `internal/policy` | Resolución destinatario → acciones (reenvío, catch-all, ...) |
| `internal/backend` | Interfaz y registro de backends externos |
| `internal/smtpd` | Servidores SMTP MX y submission *(pendiente)* |
| `internal/delivery` | Despachador y cliente SMTP saliente *(pendiente)* |
| `internal/autoreply` | Auto-responders *(pendiente)* |
| `internal/tempbox` | Buzón temporal *(pendiente)* |
| `internal/lists` | Listas de correo *(pendiente)* |
| `internal/dnshelp` | Asistente de registros DNS *(pendiente)* |
| `internal/admin` | API y UI de administración *(pendiente)* |
| `internal/stats` | Contadores de uso y consulta *(pendiente, paso 1.10)* |

## Desarrollo

Requisitos: Go 1.22 o superior.

```sh
cp config.example.json config.json
go build ./...
go test ./...
go run ./cmd/virtualbox -config config.json
```

Para probar con puertos reales (25/587) use Docker o WSL; producción debe ser Linux con IP fija, PTR coherente y puerto 25 abierto.

### Flujo de trabajo

`master` es la rama estable y está protegida por convención: **nada se sube directamente a `master`**. Cada paso del roadmap, feature, issue o fix se desarrolla en su propia rama y se integra mediante Pull Request. Detalles y convención de nombres en [CLAUDE.md](CLAUDE.md).

## Licencia

[Apache License 2.0](LICENSE).

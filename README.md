# virtualbox

Buzón virtual de correo electrónico: un servidor de correo (MTA) para dominios registrados que **recibe, redirige, responde y envía, pero no guarda correo**. El almacenamiento se delega a servidores externos (IMAP, LMTP, webhook, S3, archivo).

> **Estado:** en desarrollo temprano. Ya recibe correo por SMTP (puerto 25 con STARTTLS opcional), lo guarda en una cola persistente, resuelve alias y lo reenvía por SMTP a los MX de destino con reintentos y rebotes (DSN). **Aún sin SRS ni DKIM** (paso 1.5): los reenvíos conservan el remitente original del sobre y pueden fallar SPF. Vea [ROADMAP.md](ROADMAP.md) y [TODO.md](TODO.md).

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
| `internal/queue` | Interfaz de la cola y `FileQueue` (cola en ficheros con fsync), política de reintentos |
| `internal/dsn` | Generación de DSN/rebotes (RFC 3464) |
| `internal/policy` | Resolución destinatario → acciones: `AliasResolver` (alias, varios destinos, `+tag`, catch-all) y `StaticResolver` mínimo |
| `internal/store` | Almacén de configuración en SQLite (dominios, alias y destinos); implementa `policy.Directory` |
| `internal/backend` | Interfaz y registro de backends externos |
| `internal/smtpd` | Servidor SMTP MX (:25) con `go-smtp`; submission *(pendiente)* |
| `internal/delivery` | Despachador (reparto por destino, reexpansión de alias locales, reintentos, DSN) y cliente SMTP saliente (lookup MX, STARTTLS oportunista) |
| `internal/autoreply` | Auto-responders *(pendiente)* |
| `internal/tempbox` | Buzón temporal *(pendiente)* |
| `internal/lists` | Listas de correo *(pendiente)* |
| `internal/dnshelp` | Asistente de registros DNS *(pendiente)* |
| `internal/admin` | API y UI de administración *(pendiente)* |
| `internal/stats` | Contadores de uso y consulta *(pendiente, paso 1.10)* |

## Alias y enrutamiento

Los dominios, alias y destinos viven en una base SQLite (`db_path`, por defecto `<data_dir>/virtualbox.db`). Al arrancar, `config.json` **siembra** lo que falte (dominios, `catch_all` y `aliases`) sin sobrescribir ni borrar reglas ya existentes, de modo que los cambios posteriores hechos por la herramienta de administración sobreviven a los reinicios.

Para cada destinatario el resolver prueba, en orden:

1. la parte local exacta (`ana+news` puede tener su propia regla);
2. la parte local base sin `+etiqueta` (`ana+compras@…` usa la regla de `ana`);
3. el catch-all del dominio.

Si nada coincide, el RCPT se rechaza con `550 User unknown` durante la sesión SMTP; un dominio no registrado da `550 Relay access denied`. Un alias puede tener varios destinos (se entregan como un único reenvío con varios `Targets`, sin duplicados). La parte local no distingue mayúsculas. La etiqueta usada y el tipo de coincidencia quedan en `Action.Params` (`match`, `tag`).

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

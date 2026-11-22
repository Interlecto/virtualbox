# ROADMAP

Cada fase se divide en pasos; **cada paso se desarrolla en su propia rama** (`roadmap/<fase>-<paso>`) y se integra en `master` por Pull Request. Ver convención en [CLAUDE.md](CLAUDE.md). El detalle fino y los pendientes menores están en [TODO.md](TODO.md). Las ideas aún no comprometidas están en el [Wishlist](#wishlist-y-backlog-de-ideas) al final de este documento.

Leyenda: ✅ hecho · 🚧 en curso · ⬜ pendiente

## Fase 0 — Esqueleto ✅
- ✅ Módulo Go, estructura de paquetes e interfaces (`queue`, `policy`, `backend`, `config`)
- ✅ Documentación, licencia Apache 2.0, CI

## Fase 1 — MVP de recepción y entrega ⬜
Objetivo: recibir correo para un dominio registrado, redirigirlo y entregarlo, con identidad DNS correcta, protección básica y estadísticas de uso.
- ✅ 1.1 Cola persistente en disco (ficheros con fsync y rename atómico, reintentos con backoff, expiración y generador de DSN)
- ✅ 1.2 Listener MX (:25) con `emersion/go-smtp`: acepta solo dominios registrados, límites de tamaño, destinatarios y conexiones (total y por IP), STARTTLS, cabecera `Received`
- ✅ 1.3 Resolver de políticas: alias, redirección múltiple, catch-all y subdirecciones `+tag` (almacén de config en SQLite)
- ✅ 1.4 Cliente SMTP saliente: lookup MX, TLS, reintentos
- ⬜ 1.5 Firma DKIM por dominio y SRS para reenvíos
- ⬜ 1.6 Verificación entrante de SPF, DKIM y DMARC
- ⬜ 1.7 Asistente y diagnóstico DNS: generar y verificar MX, SPF, DKIM, DMARC y PTR; comprobar presencia en listas negras; informe de entregabilidad por dominio
- ⬜ 1.8 Certificados TLS automáticos (ACME)
- ⬜ 1.9 Protección entrante: greylisting, consulta DNSBL y *tarpit*, siempre con rechazo en la sesión SMTP
- ⬜ 1.10 Estadísticas básicas: contadores internos de uso, consulta por CLI (`virtualbox stats`) y API (`GET /api/v1/stats`), sin servicio externo. Ver [docs/ESTADISTICAS.md](docs/ESTADISTICAS.md)

## Fase 2 — Envío autenticado y relay ⬜
- ⬜ 2.1 Submission (:587 STARTTLS, :465 TLS implícito) con autenticación
- ⬜ 2.2 Autorización remitente ↔ credencial (anti open-relay)
- ⬜ 2.3 Límites de tasa por credencial, dominio e IP
- ⬜ 2.4 Relay "como si partiera del buzón" (reescritura de sobre y `From`, firma DKIM)
- ⬜ 2.5 Salida por *smarthost* o IP de origen por dominio (reputación separada; SES, Mailgun, etc.)

## Fase 3 — Backends externos y buzón temporal ⬜
- ⬜ 3.1 Backend LMTP
- ⬜ 3.2 Backend IMAP APPEND
- ⬜ 3.3 Backend webhook HTTP
- ⬜ 3.4 Backend S3 / archivo
- ⬜ 3.5 Buzón temporal: almacén local con cuota y barrido por TTL
- ⬜ 3.6 Acceso al buzón temporal: API HTTP (y opcionalmente IMAP/POP3 de solo lectura)
- ⬜ 3.7 Webhooks de eventos (recibido, entregado, rebotado, rechazado, baja)
- ⬜ 3.8 Trazabilidad de mensajes: registro de metadatos (nunca el cuerpo) y búsqueda de "qué pasó con este correo"

## Fase 4 — Auto-responders ⬜
- ⬜ 4.1 Motor de autorespuesta (RFC 3834: `Auto-Submitted`, remitente vacío)
- ⬜ 4.2 Anti-bucle y anti-backscatter (solo si SPF/DKIM pasan; una respuesta por remitente cada N días; no responder a listas ni masivos)
- ⬜ 4.3 Programación (fechas de vacaciones) y plantillas

## Fase 5 — Listas de correo ⬜
- ⬜ 5.1 Listas de difusión (solo propietarios publican)
- ⬜ 5.2 Listas de discusión (miembros publican, moderación opcional)
- ⬜ 5.3 Suscripción con doble confirmación y baja (`List-Unsubscribe`, RFC 2369 y 8058)
- ⬜ 5.4 Rebotes con VERP y limpieza automática de miembros
- ⬜ 5.5 Reescritura de `From` para dominios con DMARC `p=reject`
- ⬜ 5.6 Digest y archivo hacia backend externo

## Fase 6 — Endurecimiento y operación ⬜
- ⬜ 6.1 Integración antispam/antivirus (milter: rspamd, ClamAV)
- ⬜ 6.2 MTA-STS, DANE y TLSRPT; reportes DMARC agregados
- ⬜ 6.3 API REST y UI de administración (incluye panel de estadísticas)
- ⬜ 6.4 Exportación de métricas Prometheus (`/metrics`), series históricas con retención configurable y logs estructurados
- ⬜ 6.5 Alta disponibilidad: MX secundario
- ⬜ 6.6 Empaquetado: imagen Docker, unidad systemd, guía de despliegue

> **Estadísticas en cada fase:** todo paso que añada una función visible (acción de política, backend, lista, autoresponder, regla de protección) debe registrar sus contadores en el módulo de estadísticas (paso 1.10) como parte de su propio PR.

---

## Wishlist y backlog de ideas

Ideas útiles aún no comprometidas con una fase. No tienen orden ni fecha. Las marcadas **→ N.N** ya fueron promovidas al flujo principal del roadmap.

**Proceso:** una idea sube del wishlist al roadmap en un PR de documentación (`docs/…`) que le asigne fase y número de paso. Antes de desarrollarla conviene abrir un issue en GitHub que concrete el alcance.

### Enrutamiento y entrega
- Alias con subdirecciones `+tag` y reglas por etiqueta **→ 1.3**
- Alias desechables que se crean, revocan o caducan solos
- Reglas condicionales estilo Sieve (por remitente, asunto, tamaño o cabecera)
- *Fallback* entre backends si el principal está caído
- Entrega con *fan-out* (copia al buzón y otra a un archivo con retención)
- Ventanas de entrega (retener y liberar en horario)

### Seguridad y reputación
- Greylisting y *tarpit* **→ 1.9**
- Listas de bloqueo (DNSBL) **→ 1.9**
- Cuarentena temporal en el buzón temporal (en vez de rechazar el correo dudoso)
- Contraseñas de aplicación, 2FA para el panel y claves API con alcance limitado
- Rotación automática de claves DKIM con selectores nuevos y publicación asistida en DNS
- Detección de cuentas comprometidas (volumen o patrón anómalo de envío) con suspensión automática
- Control de DSN/bounces por dominio para evitar *backscatter*

### Envío
- Salida por *smarthost* o IP por dominio **→ 2.5**
- Calentamiento de IP (*warm-up*) con límites progresivos
- Envío por API HTTP (`POST /send`) y plantillas
- Seguimiento opcional de aperturas y clics en listas de difusión, con consentimiento
- Cola con prioridades (transaccional antes que difusión masiva)

### Listas
- Segmentos y campos de miembro para difusión personalizada
- Moderación por correo o web y listas privadas con aprobación de ingreso
- Importación y exportación de miembros; bajas globales
- Publicación en la lista por API

### Integración y automatización
- Webhooks de eventos **→ 3.7**
- Reenvío a mensajería (Telegram, Slack, Matrix) con resumen del correo
- Extracción de contenido: solo adjuntos a un almacén, o conversión del correo a JSON por webhook
- Dirección de captura (*inbound parse*): correo a `tickets@dominio` convertido en llamada de API

### Operación
- Panel de diagnóstico DNS y entregabilidad **→ 1.7**
- Trazabilidad de mensajes (solo metadatos) **→ 3.8**
- Estadísticas básicas de uso **→ 1.10**
- Reportes DMARC y TLSRPT analizados y resumidos en el panel
- Multi-inquilino (organizaciones con dominios, cuotas y administradores separados)
- Copias de seguridad de configuración y exportación/importación de dominios
- Modo de prueba (*dry-run*): simular cómo se enrutaría un mensaje antes de activar una regla
- Auditoría de cambios administrativos

### Protocolos adicionales
- Acceso de solo lectura al buzón temporal por IMAP, POP3 o JMAP
- Soporte de ARC para que reenvíos y listas no rompan la autenticación
- DANE y MTA-STS tanto entrante como saliente (parcialmente en 6.2)

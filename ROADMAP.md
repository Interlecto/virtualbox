# ROADMAP

Cada fase se divide en pasos; **cada paso se desarrolla en su propia rama** (`roadmap/<fase>-<paso>`) y se integra en `master` por Pull Request. Ver convención en [CLAUDE.md](CLAUDE.md). El detalle fino y los pendientes menores están en [TODO.md](TODO.md).

Leyenda: ✅ hecho · 🚧 en curso · ⬜ pendiente

## Fase 0 — Esqueleto ✅
- ✅ Módulo Go, estructura de paquetes e interfaces (`queue`, `policy`, `backend`, `config`)
- ✅ Documentación, licencia Apache 2.0, CI

## Fase 1 — MVP de recepción y entrega ⬜
Objetivo: recibir correo para un dominio registrado, redirigirlo y entregarlo, con identidad DNS correcta.
- ⬜ 1.1 Cola persistente en disco (fsync, reintentos con backoff, expiración y DSN)
- ⬜ 1.2 Listener MX (:25): aceptar solo dominios registrados, límites de tamaño y conexiones, STARTTLS
- ⬜ 1.3 Resolver de políticas: alias, redirección múltiple y catch-all (almacén de config en SQLite)
- ⬜ 1.4 Cliente SMTP saliente: lookup MX, TLS, reintentos
- ⬜ 1.5 Firma DKIM por dominio y SRS para reenvíos
- ⬜ 1.6 Verificación entrante de SPF, DKIM y DMARC
- ⬜ 1.7 Asistente DNS: generar y verificar MX, SPF, DKIM, DMARC
- ⬜ 1.8 Certificados TLS automáticos (ACME)

## Fase 2 — Envío autenticado y relay ⬜
- ⬜ 2.1 Submission (:587 STARTTLS, :465 TLS implícito) con autenticación
- ⬜ 2.2 Autorización remitente ↔ credencial (anti open-relay)
- ⬜ 2.3 Límites de tasa por credencial, dominio e IP
- ⬜ 2.4 Relay "como si partiera del buzón" (reescritura de sobre y `From`, firma DKIM)

## Fase 3 — Backends externos y buzón temporal ⬜
- ⬜ 3.1 Backend LMTP
- ⬜ 3.2 Backend IMAP APPEND
- ⬜ 3.3 Backend webhook HTTP
- ⬜ 3.4 Backend S3 / archivo
- ⬜ 3.5 Buzón temporal: almacén local con cuota y barrido por TTL
- ⬜ 3.6 Acceso al buzón temporal: API HTTP (y opcionalmente IMAP/POP3 de solo lectura)

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
- ⬜ 6.3 API REST y UI de administración
- ⬜ 6.4 Métricas Prometheus y logs estructurados
- ⬜ 6.5 Alta disponibilidad: MX secundario
- ⬜ 6.6 Empaquetado: imagen Docker, unidad systemd, guía de despliegue

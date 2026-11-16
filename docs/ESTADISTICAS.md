# Estadísticas de uso

Diseño de las estadísticas básicas (ROADMAP paso 1.10). Objetivo: saber **qué tanto se usa cada función** del servidor sin depender de un servicio externo. Un sistema más sofisticado (Prometheus, Grafana) se podrá conectar después (paso 6.4), pero no es necesario.

## Principios

1. **Autocontenido**: los contadores viven en el propio servidor y se consultan por CLI y API sin configurar nada más.
2. **Solo agregados, nunca contenido**: se cuentan eventos, no se guardan cuerpos, asuntos ni direcciones completas de terceros. Las dimensiones son dominio registrado, tipo de acción, backend, lista, credencial (por identificador) y motivo.
3. **Barato**: contadores en memoria con volcado periódico a SQLite (cubetas por hora). El camino caliente de SMTP solo hace un incremento atómico.
4. **Cada función registra lo suyo**: quien añade una función añade sus contadores en el mismo PR.

## Qué se mide

| Área | Contadores | Dimensiones |
|---|---|---|
| Recepción (MX) | conexiones, mensajes aceptados, rechazados, diferidos, bytes | dominio, motivo de rechazo (dominio desconocido, destinatario desconocido, tamaño, greylist, DNSBL, SPF/DKIM/DMARC) |
| Enrutamiento | resoluciones por tipo de acción (`forward`, `store`, `tempbox`, `autoreply`, `list`), uso de catch-all, uso de `+tag` | dominio, acción |
| Entrega | entregados, reintentos, rebotes permanentes, expirados | destino (backend o dominio remoto), código de resultado |
| Envío (submission) | mensajes enviados, rechazados por límite de tasa, fallos de autenticación | dominio, credencial |
| Relay | mensajes reenviados como buzón | dominio, credencial |
| Auto-responders | respuestas enviadas, suprimidas (límite, bucle, remitente vacío) | dominio, regla |
| Buzón temporal | mensajes recibidos, expirados por TTL, rechazados por cuota, ocupación | dominio, buzón |
| Listas | mensajes publicados, entregas a miembros, altas, bajas, rebotes | lista |
| Cola | profundidad actual, antigüedad del mensaje más viejo | — |
| Backends | latencia media y errores por backend | backend |

## Cómo se consulta

### CLI

```sh
virtualbox stats                         # resumen de las últimas 24 h
virtualbox stats --since 7d              # ventana de tiempo
virtualbox stats --domain example.org    # un dominio
virtualbox stats --by action             # agrupar por tipo de acción
virtualbox stats --by reason --json      # salida para scripts
```

### API (red de administración)

```
GET /api/v1/stats?since=24h&domain=example.org&by=action
GET /api/v1/stats/queue
```

Respuesta JSON con series por cubeta (hora o día) y totales. Solo accesible con credencial de administración.

### Integración opcional

El paso 6.4 añade `GET /metrics` en formato Prometheus a partir de los mismos contadores, y el 6.3 un panel en la UI. No hace falta ninguno para tener estadísticas.

## Retención

- Cubetas horarias: 30 días (configurable).
- Cubetas diarias: 13 meses (configurable).
- El barrido de retención se ejecuta junto al de TTL del buzón temporal.

## Privacidad

Las estadísticas no incluyen direcciones de remitentes ni destinatarios externos. Para depuración de un mensaje concreto existe la trazabilidad por metadatos (paso 3.8), con retención propia y desactivable.

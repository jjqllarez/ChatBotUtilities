# AGENTS.md

## Contexto del proyecto

Bot de **WhatsApp** escrito en **Go**, usando la librería [`go.mau.fi/whatsmeow`](https://github.com/mau-mau/whatsmeow).
Funciones:
1. Responde a **empleados** que le escriben: obtiene el número del remitente, lo busca en la tabla `empleados` de
   Supabase (campo `telefono`) y, si es un empleado activo, lo saluda (`Hola {nombre_completo}`). Si no es empleado, no
   responde (silencioso).
2. **Emisión de cotizaciones desde WhatsApp**: un empleado escribe `/cotizar` y el bot guía un flujo (forma de pago →
   vehículo → precio → plan/inicial → cliente → confirmar) que guarda la cotización en Supabase reutilizando los **RPC y
   tablas del proyecto CRM Astro** (`capital - cotizaciones`), que comparte la **misma base de datos**.

Donde no se indique lo contrario, todos los comandos se ejecutan desde el directorio del módulo (`go/`).

## Stack y decisiones clave

- **whatsmeow** para el protocolo de WhatsApp (multidispositivo).
- **Supabase + PostgREST** como base de datos, usando la **service-role key** vía sus endpoints REST.
  No se usa conexión SQL directa (no hay DSN/password en `.env`).
- La sesión de WhatsApp se guarda en Supabase mediante un **container custom** que implementa las interfaces de
  `store` (`store.DeviceContainer`, `store.AllSessionSpecificStores`, `store.LIDStore`) sobre REST.
- El QR de vinculación se muestra en terminal (ASCII) y también en un servidor HTTP local (`/qr`) para escanearlo cómodo
  desde el móvil.
- La **emisión de cotizaciones** reutiliza RPCs/Edge Functions existentes del CRM vía PostgREST:
  `obtener_versiones_completas`, `buscar_clientes`, `insertar_cliente_empleado`, `planes_financiamiento` (tabla),
  Edge Function `motorjson` (cálculo de planes), `insertar_cotizacion_empleado` (RPC puente propio) y
  `obtener_detalle_cotizacion`.

## Estructura

```
go/
  .env                              # credenciales Supabase + OpenRouter + QR_PORT (NO versionar)
  .gitignore                        # ignora .env y bot.exe
  go.mod / go.sum                   # módulo "bot"
  main.go                           # carga config, crea cliente/container, arranca bot
  assets/                           # web components + logo (capital-motors-cotizacion.js, vehicle-card.js, dongfeng.png)
  supabase/migrations/              # DDL/RPC de las tablas del bot (whatsmeow_*, bot_chat_history, bot_chat_state, insertar_cotizacion_empleado)
  internal/config/config.go         # leer .env
  internal/supabase/client.go       # cliente REST PostgREST + helpers de bytea + RPC/EdgeFunction
  internal/supabase/container.go|store.go|lidmap.go   # container custom de sesión whatsmeow (REST)
  internal/empleados/empleados.go   # búsqueda de empleado por teléfono (normaliza a dígitos)
  internal/cotizaciones/            # catálogo, planes/motorjson, emitir, listar, detalle (RPCs del CRM)
  internal/llm/client.go            # cliente OpenRouter (chat + tools + transcripción audio)
  internal/pdf/browser.go|cotizacion.go|vehiclecard.go # PDF/PNG de cotización y card con Chromium (+ fallback fpdf)
  internal/bot/bot.go               # client whatsmeow, QR, eventos, manejo de mensajes, re-vinculación
  internal/bot/cotizacion_flow.go   # máquina de estados del flujo /cotizar y /listar por chat
  internal/bot/assistant.go         # asistente LLM con tools y agent loop
  internal/bot/history.go|state.go  # memoria persistente + estado de conversación (Supabase)
  internal/bot/guards.go            # anti-baneo (cola de salida, ritmo, cuota diaria)
```

## Comandos

- Compilar/verificar: `go build ./...` y `go vet ./...` *(correr siempre después de cambios)*.
- Formatear: `gofmt -w .`
- Ejecutar: `go run .` o `go build -o bot.exe .` y luego `bot.exe`.

## Cómo funciona el guardado de sesión (importante)

La sesión de whatsmeow NO se guarda como archivo: se persiste en las tablas `whatsmeow_*` de Supabase a través del
container custom (`internal/supabase/*`). En cada arranque se llama `container.GetAllDevices()`; si hay un dispositivo
guardado se reutiliza, si no se genera un QR nuevo.

- **Re-vinculación automática**: si la sesión expira o el usuario la cierra (evento `events.LoggedOut`), el bot borra el
  dispositivo guardado y genera un QR nuevo en el mismo proceso.
- Las llamadas REST se hacen con la service-role key (que **omite RLS**).

## ⚠️ Gotchas que debes conocer

1. **`bytea` se envía en HEX, no base64.** Este PostgREST **no** decodifica base64 en columnas `bytea`; si mandas un
   `[]byte` en JSON lo guarda como texto ASCII crudo, rompiendo los `CHECK (length(...)=N)` de whatsmeow (error 23514).
   Por eso `client.go` convierte todo `[]byte` a `"\x"+hex` antes de enviar (`normalizeBytes`) y `GetBytes` decodifica
   el prefijo `\x` (o base64) al leer. **No cambies esto.**
2. **PostgREST cache**: cuando se crean tablas nuevas, el cache de esquema de PostgREST puede no verlas (404 intermitente
   de `whatsmeow_*`). Forzar recarga:
   ```sql
   SELECT pg_notify('pgrst','reload schema');
   ```
   Se ejecuta con `supabase db query --linked` (ver "CLI de Supabase").
3. **El 404 con `Invoke-WebRequest`/`Invoke-RestMethod` en PowerShell es un falso positivo** (PowerShell manosea la URL,
   `select=*` → `PGRST205`). Para probar REST usa `curl.exe`:
   ```powershell
   curl.exe -H "apikey: $SUPABASE_SERVICE_ROLE_KEY" -H "Authorization: Bearer $SUPABASE_SERVICE_ROLE_KEY" "$SUPABASE_URL/rest/v1/whatsmeow_device?select=*"
   ```
 4. `types.MessageID` es alias de `string` (no tiene `.String()`); usa `string(id)`.
 5. **No uses `%,.2f` en `fmt.Sprintf`** (Go no soporta el flag de agrupación); usa `%.2f`.
 6. **`.env` NO debe tener BOM UTF-8** (rompe el parseo de godotenv → las claves no se leen). Guardar con UTF-8 sin BOM. También: si el `.env` se toca, recordar que empieza sin BOM.
 7. **Historial LLM — orden de lectura**: para dar contexto hay que traer los **últimos** mensajes en orden cronológico. Hacer `order=created_at.desc&limit=N` y luego invertir (NO `asc&limit` que devuelve los más viejos y hace que el modelo "olvide" lo reciente).
 8. **`fmt.Sprintf` con CSS**: si tu plantilla HTML lleva `%` (p. ej. `height:100%`), escápalos como `%%` para no romper el `%`-verb.
 9. **Chromium**: el render PDF/PNG/card usa el binario de `CHROME_PATH` o el `chromium_headless_shell` de ms-playwright; se levanta on-demand y se cierra. Sólo PDF/PNG; para la card se autocorta al borde (`trimPNG`).

## Emisión de cotizaciones (flujo `/cotizar`)

- Es una **máquina de estados por chat** (`internal/bot/cotizacion_flow.go`), map `sessions[phone]` con mutex en el `Bot`.
- Pasos: forma de pago → vehículo → tipo de precio → (crédito) plan + inicial → cliente (buscar o crear) → confirmar.
- Reutiliza RPCs del CRM (`internal/cotizaciones/*`):
  `obtener_versiones_completas`, `buscar_clientes`, `insertar_cliente_empleado`,
  tabla `planes_financiamiento`, Edge Function `motorjson` y `obtener_detalle_cotizacion`.
- **Auth del insert (crítico)**: el trigger `auto_fill_cotizacion_fields` de `cotizaciones` requiere `auth.uid()`
  (si es NULL lanza "Usuario no autenticado") y rellena `empleado_id`/`socio_comercial_id`. Por eso NO se llama
  `insertar_cotizacion` con service-role a secas. Se usa el **RPC puente propio** `insertar_cotizacion_empleado`
  (SECURITY DEFINER) que con `set_config('request.jwt.claim.sub', p_user_id, true)` inyecta el `user_id` del empleado
  para que el trigger funcione, y luego reutiliza `insertar_cotizacion`. El bot obtiene el `user_id` de `empleados` al
  identificar al empleado por teléfono.
- El número de presupuesto se genera en Go como `COT-YYMMDD-XXX`.
- **Generación de PDF + imagen (obligatorio en cada cotización)**: se renderiza el **mismo web component**
  `capital-motors-cotizacion.js` (copia en `assets/`) con **Chromium headless por línea de comandos**
  (`--headless=new --print-to-pdf / --screenshot`), inyectándole los datos del detalle
  (`obtener_detalle_cotizacion`). Código en `internal/pdf/browser.go` (PDF `RenderPDF` + vista previa `RenderPNG`).
  Cada vez que se genera una cotización se envían **PDF + imagen** (vista previa) siempre
  (`sendCotizacionAttachments`); el LLM **no decide** si mandar la imagen.
  Si el navegador falla, fallback a `fpdf` (`internal/pdf/cotizacion.go`). Chromium se levanta **on-demand** y se cierra.
  - Binario: env `CHROME_PATH`, o se auto-detecta `chromium_headless_shell` de ms-playwright (dev).
  - Assets necesarios en `assets/`: `capital-motors-cotizacion.js`, `vehicle-card.js` y `dongfeng.png` (copiados desde
    el CRM; no mover el original). El logo se inyecta como data-URI porque el componente hardcodea `/dongfeng.png`.
- **Ficha/card de vehículo** (`internal/pdf/vehiclecard.go`): el web component `vehicle-card-pro` (sin botón
  "Consultar") renderizado como PNG. El prefijo de precio se autocorta al tamaño exacto de la card (`trimPNG`).
  Solo se envía cuando el usuario la pide expresamente ("foto del vehículo", "card", "ficha"); NO en el flujo de
  cotización.

## Asistente LLM (OpenRouter) — comportamientos clave
- **Saludo con hora Venezuela**: el primer mensaje de cada chat (sin historial) saluda "Buenos días/tardes/noches +
  nombre" según `America/Caracas` (`greetingFor`), antes de procesar.
- **Orden directa**: si el usuario da de una vez cliente + vehículo + forma de pago + tipo de precio + plan (p. ej.
  "hazme una cotización para maria salazar cedula V5676894 paladin precio estandar arca 12 meses"):
  1. Crea el cliente automáticamente si no existe (buscar_clientes → registrar_cliente), sin preguntar.
  2. Si el modelo tiene varias versiones, pregunta UNA vez cuál; si hay una sola, la usa.
  3. Si falta la inicial en Crédito, usa el mínimo del plan.
  4. Emite con `crear_cotizacion` de inmediato, SIN confirmación.
- **Listados en texto numerado**; la **card/imagen** del vehículo solo a pedido explícito.
- **Precio personalizado solo para administradores**: admin = empleado con permiso `admin_total` en su cargo activo
  (`isAdmin` vía RPC `obtener_permisos_usuario(uid)`, con caché). Las tools `crear_cotizacion` y `enviar_ficha_vehiculo`
  aceptan `precio_personalizado` (USD) que se usa en el motor (motorjson) y en la cotización/card, pero **solo se honra
  si es admin** (seguridad en el backend; a no-admin se ignora y se usa el precio de lista de Supabase).
- **Memoria persistente**: conversación en `bot_chat_history`, estado (draft) en `bot_chat_state` (el LLM usa
  `recordar_dato`); historial recargado por turno (últimos N cronológicos). Limpieza diaria 3:00 AM (hora Venezuela)
  borra mensajes >24 h.
- **Notas de voz**: whatsmeow descarga el audio (ogg/opus) y se transcribe en español con Gemini vía OpenRouter
  (`Transcribe`, `TRANSCRIBE_MODEL`). (Pendiente de validar con una nota de voz real.)

## Tablas en Supabase

- `empleados`: existe desde antes, **no tocar**. Campos usados: `telefono` (ej. `+584248821071`), `nombre_completo`, `activo`,
  `user_id`, `socio_comercial_id`. La comparación de teléfono se hace **por dígitos** en `internal/empleados/empleados.go`.
- `whatsmeow_*` (16 tablas): creadas por el bot, para persistir la sesión. Son las tablas estándar de `sqlstore` de
  whatsmeow (device, sessions, pre_keys, identity_keys, sender_keys, app_state_*, contacts, chat_settings,
  message_secrets, privacy_tokens, nct_salt, event_buffer, retry_buffer, lid_map).
- `bot_chat_history` y `bot_chat_state`: memoria (mensajes) y estado (draft/contexto) del asistente LLM por teléfono.
- Tablas del CRM (compartidas, no modificadas por el bot): `cotizaciones`, `clientes`, `versiones_vehiculos`,
  `historial_precios`, `planes_financiamiento`, `entes_financieros`, `socio_comercial`, `marcas`, `modelos`,
  `cargos`, `historial_cargos`, `permisos`, `cargo_permiso` (roles/permisos; `admin_total` = administrador).

## CLI de Supabase

El token de acceso del CLI NO debe ir en el código; se usa vía variable de entorno. Operaciones comunes:

```powershell
$env:SUPABASE_ACCESS_TOKEN="<token>"
supabase link --project-ref <ref>   # ya enlazado en go/
supabase db query --linked "SELECT ..."   # SQL directo a la BD
supabase db query --linked "SELECT pg_notify('pgrst','reload schema');"  # recargar PostgREST
```

## Credenciales / secretos

- `.env` está en `.gitignore`. Si se versiona el repo, **nunca** subir `.env` (contiene `SUPABASE_SERVICE_ROLE_KEY`).
- `QR_PORT` (default `8080`) controla el puerto del servidor HTTP del QR.

## Despliegue (VPS) — requisitos y notas

- El bot necesita un host **siempre encendido** (un VPS) porque mantiene la conexión WebSocket 24/7 con WhatsApp;
  un host con sleep (p. ej. free tier de PaaS) rompería la sesión.
- **Parte persistente:** la sesión de WhatsApp vive en Supabase (`whatsmeow_*`); al reiniciar el proceso el bot
  recupera la sesión y **no pide QR**.
- **PDF por navegador:** se usa Chromium de forma **transitoria** (solo al cotizar). Requisitos en el VPS:
  - Un binario Chromium/Chrome accesible; configurar la env `CHROME_PATH` con su ruta en producción.
  - La carpeta `assets/` (web component + logo) debe existir junto al `bot.exe` (o resolverse vía cwd/padre).
  - No se necesita Node/Playwright para el bot (A1 usa Chrome CLI directo).
- RAM: base del bot Go es baja (~30-80 MB); Chromium solo se suma durante los pocos segundos de cada PDF (~200-400 MB
  pico). Un VPS de 1 GB va sobrado.

## Variables de entorno del asistente LLM

- `OPENROUTER_API_KEY`, `OPENROUTER_MODEL` (default `openrouter/auto`)
- `TRANSCRIBE_MODEL` (default `google/gemini-2.5-flash-lite`) — transcripción de notas de voz (Gemini vía OpenRouter)
- `WA_MIN_GAP_MS` (default 1200), `WA_MAX_DAILY_MSGS` (default 1500) — anti-baneo
- `HISTORY_CLEAN_HOUR` (default 3), `HISTORY_RETAIN_HOURS` (default 24) — limpieza de memoria
- `CHROME_PATH` (Chromium para PDF/PNG/card), `QR_PORT` (default 8080)

## Prueba manual del bot

1. `go run .` (o `bot.exe`).
2. Si no hay sesión: la consola muestra el QR ASCII y `http://<IP-local>:8080/qr`. Escanear con
   WhatsApp → Dispositivos vinculados → Vincular dispositivo.
3. La sesión se guarda en `whatsmeow_device`.
4. Desde el teléfono, un empleado le escribe al bot → saluda según la hora de Venezuela y responde con el asistente.

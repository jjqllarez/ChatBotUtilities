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
  internal/bot/cotizacion_flow.go   # flowManager legacy (/cotizar y /listar): máquina de estados + tools
  internal/bot/cotizacion_wrapper.go# CotizacionFlow: wrapper Flow sobre el flowManager legacy (sin tocar su lógica)
  internal/bot/flow.go              # interfaz Flow, FlowResult, FlowRegistry (registro + caché bot_flows 30 min)
  internal/bot/flow_catalogo.go     # CatalogoFlow (catalogo_vehiculos, FlowComposable)
  internal/bot/flow_cliente.go      # RegistrarClienteFlow (registrar_cliente, FlowComposable)
  internal/bot/preclasificador.go   # preClasificarLLM: clasifica mensajes ambiguos usando bot_flows
  internal/bot/router.go            # router determinista V2 (classifyIntent) + handleRouterIntent con preclasificador
  internal/bot/assistant.go         # asistente LLM (sin tools en V2) + runAssistantV2
  internal/bot/history.go|state.go  # memoria persistente + estado de conversación (Supabase)
  internal/bot/guards.go            # anti-baneo (cola de salida, ritmo, cuota diaria)
  internal/bot/simmode.go           # modo simulación: bot_config.simulation + ForceSim + SimulateMessage
  cmd/simchat/main.go               # driver de pruebas SIN WhatsApp (inyecta mensajes en handleMessageV2)
```

## Modo simulación y driver de pruebas (`cmd/simchat`)

Dos mecanismos para probar el pipeline real **sin enviar nada por WhatsApp** (cero riesgo de baneo):

1. **Switch en Supabase** (`bot_config.simulation = 'true'/'false'`, tabla creada por la migración
   `supabase/migrations/20260814000000_bot_config.sql`): con `'true'`, el bot de producción procesa los
   mensajes reales por el pipeline completo (ruteo, flujos, LLM, Supabase, PDF) pero NO envía por WhatsApp:
   registra `[SIM] -> <jid>: <texto>` en consola y guarda los adjuntos (card PNG, PDF) en `sim_out/`
   (carpeta junto al binario, `/opt/whatsbot/sim_out`). El flag se cachea 10 s (`simmode.go`), cambia en
   caliente y si la lectura falla conserva el último valor conocido. Poner en `'false'` para volver a enviar.
2. **Driver `cmd/simchat`** (nuevo): proceso aparte que inyecta mensajes directo en `handleMessageV2` del
   mismo paquete, con Supabase/LLM reales, **sin abrir socket ni usar ningún cliente de WhatsApp**. Es la
   vía recomendada para que los agentes prueben ruteo/negocio por su cuenta sin molestar al usuario.
   - Se construye igual que `main.go` (`bot.New` con container Supabase de solo lectura, sin `Run()`:
     `New()` no conecta whatsmeow; `Connect()` es aparte y nunca se llama).
   - `b.ForceSim(true)` fuerza sim sin consultar el flag; `b.SimulateMessage(phone, text)` construye un
     `events.Message` fake (JID, ID `SIM-<nanotime>`, `Conversation`) y lo mete al pipeline.
   - La salida se drena por el `outWorker` normal → `simEmit` (consola + `sim_out/`).
   - Blindaje necesario: `handleMessageV2` omite `b.client.MarkRead` si `client == nil` o sim activa, y la
     ruta de audio requiere `client != nil` (no aplica a texto simulado).
   - Ejecución en el VPS (el `.env` y `sim_out/` viven en `/opt/whatsbot` y la carpeta es de `www-data`):
     ```bash
     # /tmp/simchat compilado con: go build -o /tmp/simchat ./cmd/simchat
     sudo -u www-data bash -c 'cd /opt/whatsbot && /tmp/simchat -phone 584248821071 "Catalogo" "8" "1"'
     ```
     (las comillas anidadas via plink se rompen; usar un script `sudo /tmp/run_simchat.sh` como wrapper).
   - Verificado end-to-end (2026-08-14): "Catalogo" → lista completa; "ficha del vehiculo 30" → card PNG en
     `sim_out/`; flujo `/cotizar 2 17 3 1 50% V-16573081 si` → COT-260814-528 (id 44) con PDF+PNG reales,
     precio flota 36213.14 e inicial 50% = 18106.57, todo sin tocar WhatsApp.
   - Nota: en el VPS el módulo Go está en la **raíz** del repo
     (`/home/adminvps/services/ChatBotUtilities`), no dentro de `go/`. El `go/` del árbol solo existe
     localmente/en docs; compilar y desplegar es `go build ./... && sudo ./deploy.sh` desde la raíz.

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

## Fallas corregidas con el probe local (2026-08-13)

Fallas reales encontradas probando con el bot cliente local (`probe.go`, ver `probe-local.md`)
y corregidas en caliente:

1. **`stepCliente` se quedaba pegado si el cliente no existía** (`internal/bot/cotizacion_flow.go`):
   si `BuscarClientes` devolvía 0 resultados y el texto no era formato `TipoDoc,Cedula,Nombre,Telefono`,
   el bot respondía "No encontré ese cliente..." y quedaba esperando sin registrar nada.
   **Fix**: `parseNuevoCliente` extrae nombre + teléfono/cédula de texto libre (regexp
   `reClienteTelefono`, `reClienteDoc`), llama `CrearCliente` y continúa a `askConfirmar`; si no
   hay datos suficientes, recién ahí pide el formato `TipoDoc,Cedula,Nombre,Telefono`.

2. **`/cancelar` mudo tras reinicio del bot** (`internal/bot/cotizacion_flow.go`):
   `cancel()` solo revisaba la sesión **en memoria** (`f.sessions[phone]`). Tras un reinicio con un
   flujo persistido en `bot_chat_state`, el `/cancelar` no recargaba el borrador, no respondía nada y
   `handleCommand` devolvía `true`, tragándose el mensaje (el usuario no sabía si se canceló).
   **Fix**: `cancel()` ahora usa `ensureSession` (recarga el borrador persistido), limpia el draft y
   **siempre** responde "Cotización cancelada.".

3. **Identificación del remitente (LID vs número de teléfono)** (`internal/bot/bot.go`):
   WhatsApp migra los chats a LIDs (`@lid`). El handler resuelve el teléfono real con
   `ev.Info.SenderAlt.ToNonAD().User` (si está presente), si no con `ev.Info.Sender.ToNonAD().User`.
   Verificado con el probe: `sender=...@lid` + `senderAlt=584248821071@s.whatsapp.net` →
   `phone=584248821071` → coincide con `empleados.telefono` (+584248821071) por dígitos.
   **No usar `UserID` del empleado como número**: `UserID` es un UUID que solo sirve como
   `p_user_id` en los RPC de emisión, nunca para construir JIDs o buscar por teléfono.
   Se dejaron líneas `DEBUG msg ... phone= ... sender= ... senderAlt= ...` en `handleMessage`
   para diagnóstico (también loguea si el remitente no es empleado activo).

4. **Anti-baneo reforzado** (para no arriesgar ninguna de las 2 cuentas: la de producción y la del probe):
   - Ya existía: separación mínima entre envíos (`WA_MIN_GAP_MS`, default 1200 ms) y cuota diaria por
     chat (`WA_MAX_DAILY_MSGS`, default 1500) en `internal/bot/guards.go`.
   - Nuevo: pausa de 3 s antes de reconectar tras un disconnect (`Run` en `internal/bot/bot.go`) para
     no parecer una reconexión automatizada.
   - Nuevo: throttle de 3 s entre `/send` del probe (`probe.go`) para evitar ráfagas accidentales.
   - Recomendación operativa: NO hacer pruebas en ráfagas ni repetir el mismo mensaje muchas veces
     seguidas; cada prueba es 1 mensaje manual espaciado.

5. **Supabase 500 intermitente en sync de app state** (observado, no corregido): a veces
   `Failed to sync app state ... whatsmeow_app_state_mutation_macs -> 500`. No rompe el bot
   (reintenta), pero si reaparece, revisar si PostgREST sobrecarga la BD. Recargar esquema:
   `supabase db query --linked "SELECT pg_notify('pgrst','reload schema');"`.

6. **Interrupciones de catálogo en el flujo** (`internal/bot/cotizacion_flow.go`):
   en `stepVehiculo`/`stepPrecio`/`stepPlan`/`stepInicial`/`stepPickCliente`/`stepConfirmar`,
   si `parseIndex` falla y el texto es un pedido de catálogo (`esPeticionCatalogo`: "lista de
   vehiculos", "precios de los carros", "dame la lista de vehiculos con precios", "que vehiculos
   hay", etc.), el bot llama `toolListar`/`listar_catalogo` directamente en vez de mandar el texto
   al LLM (que inventaba opciones falsas tipo "Elige 1 (Estandar)").

7. **Ficha/card del vehículo determinista** (`internal/bot/intent.go`):
   `parseFichaVehiculo` + regexp `reFichaVehiculo` ("ficha/card del vehículo N") se manejan en
   `handleDirectIntent` llamando `toolEnviarFicha` con `{"version_id":N}` directamente. Antes el
   LLM regurgitaba "¿Tipo de precio para PALADIN MID?" en vez de enviar la imagen de la card.

8. **Cédula con letra obligatoria** (`internal/bot/cotizacion_flow.go`):
   `parseDoc` acepta `V16573081`, `V-16573081` y `V 16573081` pero **rechaza un número sin letra**
   (no se puede saber el tipo de documento). En `stepCliente` si el texto trae nombre+teléfono sin
   cédula, se pasa a `stepClienteCedula` pidiendo "Escribe la cédula de X (ej: V-12345678)."; si el
   texto no tiene letra, pide "Escribe la cédula con su letra (ej: V-16573081). La letra puede ser
   V, E, J, P o G.". `parseNuevoCliente` ya no usa el teléfono como número de documento.

9. **Duplicado de cliente al crear (409 23505)** (`internal/cotizaciones/catalogo.go`):
   el RPC `insertar_cliente_empleado` falla con 409 `unq_cliente_por_socio` si ya existe un cliente
   con el mismo (socio, tipo_documento, número). `CrearCliente` ahora detecta el error (409/23505)
   y llama `BuscarClientePorDocumento` (select REST por esos 3 campos) para **reutilizar el id del
   cliente existente** en vez de mostrar "No pude registrar el cliente: ...". Verificado: la cédula
   V-16573081 (cliente id 21) con nombre distinto reutilizó el id 21 y emitió COT-260813-006.

10. **`parseIndex` solo aceptaba dígitos puros** (`internal/bot/cotizacion_flow.go`):
    un texto como `"La 20"` fallaba y caía al LLM, que respondía la pregunta del paso **sin
    avanzar el estado** del flujo; el siguiente `"3"` se reinterpretaba como índice de vehículo
    (#3 = PALADIN UPR) en vez de "Flota", **repitiendo el prompt "¿Tipo de precio..." con el
    vehículo equivocado** (visto en el chat manual de las 11:10; terminó en COT-260813-007 con
    PALADIN UPR en vez de la Navara pedida).
    **Fix**: `parseIndex` ahora extrae el **primer número** del texto (regexp `\d+`), aceptando
    `"20"`, `"La 20"`, `"el vehículo 3"`, `"opción 2"`, etc. Además `stepPrecio` y `stepFormaPago`
    tienen fallback numérico (`"el 3"` → flota, `"la 2"` → crédito) para no delegar al LLM algo
    que pertenece al flujo. Verificado en vivo: `"La 20"` → Navara 4WD STD, `"3"` → Flota →
    plan → COT-260813-008.

11. **Inicial acepta porcentaje o moneda** (`internal/bot/cotizacion_flow.go`, `parseInicial`):
    en `stepInicial` el usuario puede responder con **porcentaje** ("50", "50%", "50 por ciento",
    "el 50") y el bot **calcula él mismo el monto** (`precio × %/100`, se convierte a USD y sigue
    el flujo en moneda), o con **monto en USD** ("25000", "$25000", "25000 USD") que se usa
    directo. Regla: número sin marcador `<= 100` se interpreta como porcentaje; `> 100` como USD.
    El prompt ahora muestra "(ej: 25000 o 50%)" y el rechazo del mínimo muestra porcentaje + USD.
    Verificado en vivo: `50%` → 19.026,78 USD (50% de la flota 38.053,56 del PALADIN MID) y
    `25000` → 25.000,00 USD.

12. **Fallo silencioso de ficha/imprimir** (`internal/bot/intent.go`): `handleDirectIntent` enrutaba
    `toolEnviarFicha`/`toolImprimirCotizacion` pero **tragaba** el error: el resultado ("No encontré la
    versión 5.") se devolvía solo al LLM (que no lo reenviaba) y el usuario no veía nada.
    **Fix**: helper `directResultError(r)` (true si el texto no contiene "enviad") → si es un error,
    `handleDirectIntent` lo envía con `sendText`. Verificado en vivo: "ficha del vehiculo 5" →
    "No encontré la versión 5." (11:46:58).

13. **Números negativos y ≤0** (`internal/bot/cotizacion_flow.go`, `firstPositiveNumber`): `parseIndex`
    y `parseInicial` extraen ahora el primer número rechazando un `-` precedente y `<= 0`
    (tests `TestFirstPositiveNumber`/`TestParseInicialFixes`). Antes "-1" se interpretaba como 1.

14. **El LLM recordaba mal el paso del flujo en curso** (`stepHint` en `cotizacion_flow.go`): cuando el
    empleado interrumpía `/cotizar` con otra petición, el asistente respondía al margen del borrador.
    **Fix**: `stepHint(ctx, phone)` devuelve un mensaje de sistema con el paso ACTIVO y cómo
    continuarlo (p. ej. "recuérdale que escriba el número del vehículo"); `bot.go` lo pasa a
    `runAssistant(..., flowHint)` en las dos llamadas y `assistant.go` lo inyecta como mensaje `system`.

15. **Failover de modelos LLM** (`internal/llm/client.go`): el modelo único fallaba en silencio
    (respuesta vacía: "hola que haces?" y "asdfghjkl" quedaron sin `assistant` en el historial).
    **Fix**: `OPENROUTER_MODEL` acepta JSON array / coma-separado / único; `Chat` itera los modelos en
    orden y salta los que fallan (error HTTP, timeout 20 s/modelo vía `chatHTTP`, respuesta vacía sin
    tool calls) con **cooldown 60 s** (`cooldown` map + mutex); `openrouter/auto` se agrega al final
    como red de seguridad. `runAssistant` usa `llmCtx` con `context.WithTimeout(150s)` para dar margen
    al failover; `main.go` loguea la lista con `SetLogger`/`Models()`. Verificado en vivo: con
    `fake-model-xyz:free` primero, el bot logueó el fallo (400) y respondió con el siguiente modelo.

16. **`isAdmin` nunca detectaba administradores** (`internal/bot/assistant.go`): el RPC
    `obtener_permisos_usuario` devuelve la columna **`codigo_permiso`**, pero `queryAdmin` solo
    revisaba `codigo|permiso|nombre|clave` → `isAdmin` devolvía siempre `false` y el precio
    personalizado (feature de admins) **nunca se aplicaba**. **Fix**: añadir `codigo_permiso` a las
    claves chequeadas. Verificado con un harness temporal contra la BD real (se borró):
    `isAdmin(JOHNATHAN)=true`, `isAdmin(Roymer, cargo VENDEDOR)=false`.
    Prueba end-to-end del guard (harness → `toolCrearCotizacion` con
    `precio_personalizado=30000`, Navara 2WD FLG flota):
    - ADMIN (JOHNATHAN): COT-260813-009 → `precio_seleccionado: 30000` (personalizado honrado).
    - VENDEDOR (Roymer): COT-260813-010 → `precio_seleccionado: 36213.14` (lista flota; el 30000 se ignoró).

17. **`BuscarClientes` no matcheaba cédula con letra** (`internal/cotizaciones/catalogo.go`): el RPC
    `buscar_clientes` busca contra `numero_documento` **sin la letra**: "V-16573081" devolvía 0
    resultados (y el flujo pedía registrar un cliente que ya existía). **Fix**: si el término es un
    documento con letra (`V/E/J/P/G` + dígitos, regexp `reDocLetra`) y la búsqueda devuelve vacío,
    se reintenta con solo los dígitos. Verificado: "V-16573081" → cliente 21.

18. **Regresión en vivo del flujo `/cotizar` completo (post-fixes 16/17, binario desplegado)**: el flujo
    guiado de punta a punta — `/cotizar` → forma de pago 2 (Crédito) → vehículo 17 (Navara 2WD FLG) →
    precio 3 (Flota 36.213,14 USD) → plan 1 (ARCA 12 MESES) → inicial `50%` (18.106,57 USD) → cliente
    **`V-16573081`** (encontrado gracias al fix 17) → confirmación → emisión COT-260813-011 (id 27) con
    **PDF + imagen** enviados y leídos. El detalle guardado en Supabase confirma `precio_seleccionado:
    36213.14` (flota), `inicial: 18106.57` (50%), plan ARCA 12 MESES, vendedor JOHNATHAN QUIJADA.

19. **Soporte de Tablas en Planes de Financiamiento** (`internal/cotizaciones/plan.go` y `internal/pdf/browser.go`):
    la respuesta `resultado_motor` devuelta por la Edge Function `motorjson` al evaluar planes contiene
    `tokens`, `bloques` y `tablas`. Sin embargo, la struct `ResultadoMotor` en `plan.go` no tenía el campo
    `Tablas []json.RawMessage `json:"tablas"``. En consecuencia, Go descartaba la clave `tablas` al guardar la
    cotización en Supabase y las cotizaciones quedaban persistidas sin tablas (`tablas: null`), haciendo que el
    Web Component `<capital-motors-cotizacion>` no renderizara la sección de tablas en el PDF/imagen.
    **Fix**:
    - Se agregó `Tablas []json.RawMessage `json:"tablas"`` a `ResultadoMotor` en `internal/cotizaciones/plan.go`.
    - Se verificó que `componentData` y `buildHTML` en `internal/pdf/browser.go` inyecten `wc.tablas = data.tablas;`.
    - Verificado con el driver de simulación (`simchat`): cotización COT-260815-008 (id 56) generó y persistió
      la tabla "Gastos Adicionales" (Concepto, Monto) con sus 4 filas correctamente en Supabase, PDF e imagen.


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
- `bot_flows`: metadatos dinámicos de los flujos conversacionales (multi-tenant por `socio_comercial`). Es la fuente de
  verdad que lee el `preClasificarLLM` para despachar mensajes ambiguos (ver Sección 9). No hay migración SQL versionada:
  las filas se insertan/editan directo en Supabase (ver 9.2/9.3).
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

- `OPENROUTER_API_KEY`, `OPENROUTER_MODEL` (default `openrouter/auto`; puede ser un solo modelo, una lista
  separada por comas o un JSON array — se prueban en orden con failover y cooldown, ver fix 15)
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

---

## 9) Sistema de Flujos Escalables (Arquitectura Modular)

### 9.1 ¿Para qué sirve la tabla `bot_flows` en Supabase?
La tabla `bot_flows` es la **fuente de verdad dinámica** de los flujos conversacionales.
- `socio_comercial` (int): Aislamiento por concesionaria/agencia (multi-tenant).
- `nombre` (text): Identificador único del flujo (ej. `cotizacion`, `catalogo_vehiculos`, `registrar_cliente`).
- `descripcion` (text): Descripción en lenguaje natural que el LLM pre-clasificador utiliza para entender la intención.
- `frases_ejemplo` (text[]): Frases típicas de los usuarios para entrenar contextualmente la clasificación.
- `activo` (bool): Permite activar/desactivar flujos por concesionaria sin redeploy.
- `orden` (int): Prioridad de evaluación.

**Propósito**: El `preClasificarLLM` lee esta tabla en tiempo real (con caché TTL de 30 min). Cuando un mensaje es ambiguo y ningún regex determinista lo clasifica, el LLM consulta los flujos de esta tabla para decidir a cuál despachar. Al insertar un flujo en la BD, **el LLM lo aprende automáticamente sin tocar código de prompt**.

### 9.2 Cómo AGREGAR un flujo nuevo

1. **Insertar en Supabase (`bot_flows`)**:
   ```sql
   INSERT INTO bot_flows (socio_comercial, nombre, descripcion, frases_ejemplo, orden)
   VALUES (1, 'seguro', 'El vendedor quiere solicitar o cotizar una póliza de seguro vehicular.', ARRAY['cotizar seguro', 'quiero seguro para la camioneta'], 60);
   ```

2. **Crear la clase del flujo en Go (`internal/bot/flow_<nombre>.go`)**:
   Implementar la interfaz `Flow` (`internal/bot/flow.go`):
   - `Nombre()` string -> devuelve `"seguro"` (debe coincidir con `bot_flows.nombre`)
   - `Tipo()` FlowTipo -> `FlowAutonomo` (lo inicia el usuario) o `FlowComposable` (también invocable como sub-rutina)
   - `Activo(ctx, phone)` bool -> consulta si hay un borrador o estado activo en `bot_chat_state`
   - `Iniciar(phone, emp)` -> envía el primer mensaje y guarda estado inicial
   - `IniciarComo(phone, emp, params)` -> si es `FlowComposable`, recibe parámetros del flujo padre
   - `Procesar(ctx, phone, emp, text)` -> máquina de estados del flujo, devuelve `FlowResult{Completado: bool, Datos: map}`
   - `Cancelar(phone)` -> limpia el estado en `bot_chat_state` al escribir `/cancelar`
   - `StepHint(ctx, phone)` -> pista textual del paso actual para el prompt del LLM de conversación

   Ejemplos reales: `flow_catalogo.go` (CatalogoFlow, `catalogo_vehiculos`, FlowComposable),
   `flow_cliente.go` (RegistrarClienteFlow, `registrar_cliente`, FlowComposable) y
   `cotizacion_wrapper.go` (CotizacionFlow, `cotizacion`, FlowAutonomo — delega todo el
   wizard al flowManager legacy de `cotizacion_flow.go`).

3. **Registrar en `main.go`**:
   ```go
   b.RegisterFlow(bot.NewSeguroFlow(b))
   ```

4. **Agregar pruebas unitarias en `internal/bot/intent_test.go`**:
   Añadir al menos 3 casos positivos y 2 negativos en `TestClassifyIntent`.

5. **Compilar y desplegar**:
   ```bash
   ./build.sh
   sudo ./deploy.sh
   ```

### 9.3 Cómo MODIFICAR un flujo existente

1. **Si cambias los activadores o la descripción**:
   Actualiza la fila correspondiente en `bot_flows` en Supabase. No requiere redeploy (el caché de 30 min se actualizará solo o al reiniciar el servicio).
2. **Si añades o cambias un paso interno**:
   Edita únicamente el archivo Go del flujo (`internal/bot/flow_<nombre>.go` o `cotizacion_flow.go`).
   - Mantén las variables de estado en `bot_chat_state` usando el prefijo propio del flujo (ej: `cot_step`, `seg_step`).
   - Asegúrate de actualizar `StepHint()` para que el LLM de conversación sepa orientar al usuario si este hace preguntas fuera del wizard.
   - Asegúrate de actualizar `Cancelar()` si agregaste nuevas claves de estado para evitar borradores huérfanos.
3. **Correr los tests**:
   `go test ./internal/bot/` **siempre** antes de hacer deploy para asegurar que las intenciones y parsers no se hayan roto.

### 9.4 Reglas de Oro de los Flujos
1. **Estado en Supabase**: Toda variable de estado del flujo se guarda en `bot_chat_state` con un prefijo único por flujo.
2. **TTL obligatorio**: Todo borrador de flujo debe tener tiempo de expiración (60 min mínimo).
3. **Cancelable**: El flujo DEBE responder a `/cancelar` limpiando todo su estado.
4. **Independiente**: Un flujo no llama métodos de otros flujos directamente — usa el registro/mecanismo de composición (`FlowComposable`).
5. **Tests primero**: Ejecutar `go test ./internal/bot/` antes de cada deploy.

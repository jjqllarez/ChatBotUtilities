package bot

import (
	"context"
	"fmt"
	htmlesc "html"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"bot/internal/correo"
	"bot/internal/supabase"
)

// ---------------------------------------------------------------------------
// Sistema de correo (PurelyMail): consumidor de `cola_correos` + digest de
// alertas por fallos de WhatsApp. Ver migración supabase/migrations.
//
//   - El productor entrega asunto + cuerpos finales (sin plantillas).
//   - La contraseña del remitente vive cifrada en `cuentas_correo.password_enc`
//     y se descifra con EMAIL_CRED_KEY (solo en el .env del bot).
//   - Reintentos: 3 intentos totales (1 inicial + 2 reintentos), hardcodeado.
//     Backoff creciente 1min / 5min entre intentos.
//   - Alertas: los fallos de WhatsApp de campañas (cobranzas + programados) se
//     agrupan (digest) y se envían a un destinatario fijo.
// ---------------------------------------------------------------------------

const (
	tablaCuentas  = "cuentas_correo"
	tablaCorreos  = "cola_correos"
	tablaIntentos = "intentos_correo"
	tablaErrores  = "errores_whatsapp"

	correoWorkerID = "whatsbot"

	// Destinatario/remitente fijos de las alertas (decisión del negocio).
	alertaPara      = "johnathan.quijada@gmail.com"
	alertaRemitente = "servidor@dongfengve.com" // debe existir en cuentas_correo
	// Fallback si el correo de alerta falla: WhatsApp directo (sin re-alerta).
	fallbackWA = "584248821071" // 04248821071 en formato internacional

	// Envío: máximo 3 intentos totales (1 inicial + 2 reintentos), hardcodeado.
	maxIntentosCorreo = 3
)

// backoffCorreos: espera creciente entre intentos (índice = intentos ya fallidos).
var backoffCorreos = []time.Duration{1 * time.Minute, 5 * time.Minute}

// cfgCorreo agrupa la configuración del sistema de correo (se fija en startCorreo).
var cfgCorreo ajustesCorreo

type ajustesCorreo struct {
	enabled     bool
	key         string
	pollSecs    int
	alertasSecs int
}

func correoFromEnv() ajustesCorreo {
	a := ajustesCorreo{
		key:         strings.TrimSpace(os.Getenv("EMAIL_CRED_KEY")),
		pollSecs:    60,
		alertasSecs: 300,
	}
	if v, err := strconv.Atoi(os.Getenv("EMAIL_POLL_SECS")); err == nil && v >= 15 {
		a.pollSecs = v
	}
	if v, err := strconv.Atoi(os.Getenv("ALERTAS_WHATSAPP_SECS")); err == nil && v >= 30 {
		a.alertasSecs = v
	}
	a.enabled = a.key != ""
	return a
}

// startCorreo arranca el consumidor de correos y el digest de alertas.
func (b *Bot) startCorreo() {
	cfgCorreo = correoFromEnv()
	if !cfgCorreo.enabled {
		b.log.Printf("Correo deshabilitado (falta EMAIL_CRED_KEY)")
		return
	}
	b.log.Printf("Correo activo (poll cada %ds, alertas cada %ds, intentos=%d, backoff=%v)",
		cfgCorreo.pollSecs, cfgCorreo.alertasSecs, maxIntentosCorreo, backoffCorreos)
	go b.correoConsumidor()
	go b.correoAlertasWorker()
}

// ---------------------------------------------------------------------------
// Consumidor
// ---------------------------------------------------------------------------

func (b *Bot) correoConsumidor() {
	interval := time.Duration(cfgCorreo.pollSecs) * time.Second
	b.correoRecuperarColgados()
	for {
		time.Sleep(interval)
		if b.simEnabled() {
			continue
		}
		b.correoProcesarPendientes()
	}
}

// correoRecuperarColgados devuelve a 'pendiente' las filas que quedaron en
// 'enviando' (p. ej. por un crash a mitad de envío) hace más de 15 min.
func (b *Bot) correoRecuperarColgados() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	limite := time.Now().Add(-15 * time.Minute).UTC().Format(time.RFC3339)
	filter := "?estado_envio=eq.enviando&locked_at=lt." + url.QueryEscape(limite)
	row := map[string]any{"estado_envio": "pendiente", "locked_at": nil, "locked_by": nil, "updated_at": time.Now().UTC().Format(time.RFC3339)}
	if err := b.supabaseUpdate(ctx, tablaCorreos, filter, row); err != nil {
		b.log.Printf("Correo: recuperando colgados: %v", err)
	}
}

func (b *Bot) correoProcesarPendientes() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nowUTC := time.Now().UTC().Format(time.RFC3339)
	q := "?select=id,socio_comercial,remitente_id,para,asunto,cuerpo_html,cuerpo_texto,tipo,intentos,fecha_programada,hora_programada" +
		"&estado_envio=eq.pendiente" +
		"&or=(proximo_intento.is.null,proximo_intento.lte." + url.QueryEscape(nowUTC) + ")" +
		"&order=prioridad.asc,id.asc&limit=200"

	rows, err := b.supa.Select(ctx, tablaCorreos, q)
	if err != nil {
		b.log.Printf("Correo: leyendo pendientes: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	b.log.Printf("Correo: %d mensajes pendientes por procesar", len(rows))
	for _, r := range rows {
		b.correoProcesarUno(r)
	}
}

func (b *Bot) correoProcesarUno(r map[string]any) {
	id := supabase.GetInt(r, "id")
	remitenteID := supabase.GetInt(r, "remitente_id")
	para := supabase.GetString(r, "para")
	asunto := supabase.GetString(r, "asunto")
	bodyHTML := supabase.GetString(r, "cuerpo_html")
	bodyTexto := supabase.GetString(r, "cuerpo_texto")
	tipo := supabase.GetString(r, "tipo")
	intentos := supabase.GetInt(r, "intentos")

	// Envío diferido: respetar fecha_programada + hora_programada (hora VE).
	if !correoEsHora(r) {
		return
	}

	// Claim: marcar 'enviando' antes de mandar (un solo worker, pero evita
	// dobles en reinicios/carreras).
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 15*time.Second)
	claimRow := map[string]any{
		"estado_envio": "enviando",
		"locked_at":    time.Now().UTC().Format(time.RFC3339),
		"locked_by":    correoWorkerID,
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
	}
	cerr := b.supabaseUpdate(claimCtx, tablaCorreos, "?id=eq."+strconv.FormatInt(id, 10), claimRow)
	claimCancel()
	if cerr != nil {
		b.log.Printf("Correo: claim id %d: %v", id, cerr)
		return
	}

	// Resolver remitente + descifrar contraseña.
	cuenta, err := b.correoCuenta(remitenteID)
	if err != nil {
		b.log.Printf("Correo: id %d sin remitente válido: %v", id, err)
		b.correoErrorDefinitivo(id, tipo, intentos, "remitente sin credenciales: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	res := cuenta.Enviar(ctx, para, asunto, bodyHTML, bodyTexto)
	cancel()

	if res.Err == nil {
		b.log.Printf("Correo enviado (id %d) a %s desde %s", id, para, cuenta.Email)
		b.correoMarcarEnviado(id, intentos)
		return
	}

	permanente := !res.Temporal && res.Codigo >= 500
	if !permanente && intentos+1 < maxIntentosCorreo {
		espera := backoffCorreos[min(int(intentos), len(backoffCorreos)-1)]
		b.log.Printf("Correo id %d falló (intento %d/%d), reintento en %s: %v",
			id, intentos+1, maxIntentosCorreo, espera, res.Err)
		b.correoMarcarReintento(id, intentos, res, espera)
		return
	}
	b.log.Printf("Correo id %d falló definitivamente tras %d intento(s): %v", id, intentos+1, res.Err)
	b.correoErrorDefinitivo(id, tipo, intentos, res.Err.Error())
}

// correoCuenta carga la cuenta remitente y descifra su contraseña.
func (b *Bot) correoCuenta(id int64) (correo.Cuenta, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	row, ok, err := b.supa.SelectOne(ctx, tablaCuentas,
		"?select=id,nombre_remitente,email,smtp_host,smtp_port,smtp_seguridad,smtp_usuario,password_enc,activo"+
			"&id=eq."+strconv.FormatInt(id, 10)+"&limit=1")
	if err != nil {
		return correo.Cuenta{}, err
	}
	if !ok {
		return correo.Cuenta{}, fmt.Errorf("cuenta %d no existe", id)
	}
	if !supabase.GetBool(row, "activo") {
		return correo.Cuenta{}, fmt.Errorf("cuenta %s inactiva", supabase.GetString(row, "email"))
	}
	pass, err := correo.Descifrar(supabase.GetString(row, "password_enc"), cfgCorreo.key)
	if err != nil {
		return correo.Cuenta{}, err
	}
	c := correo.Cuenta{
		Nombre:    supabase.GetString(row, "nombre_remitente"),
		Email:     supabase.GetString(row, "email"),
		Usuario:   supabase.GetString(row, "smtp_usuario"),
		Password:  pass,
		Host:      supabase.GetString(row, "smtp_host"),
		Port:      int(supabase.GetInt(row, "smtp_port")),
		Seguridad: supabase.GetString(row, "smtp_seguridad"),
	}
	if c.Usuario == "" {
		c.Usuario = c.Email
	}
	if c.Host == "" {
		c.Host = "smtp.purelymail.com"
	}
	if c.Port == 0 {
		c.Port = 465
	}
	if c.Seguridad == "" {
		c.Seguridad = "ssl"
	}
	return c, nil
}

func (b *Bot) correoMarcarEnviado(id, intentos int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	row := map[string]any{
		"estado_envio": "enviado",
		"fecha_envio":  time.Now().UTC().Format(time.RFC3339),
		"error_envio":  nil,
		"locked_at":    nil,
		"locked_by":    nil,
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
	}
	if err := b.supabaseUpdate(ctx, tablaCorreos, "?id=eq."+strconv.FormatInt(id, 10), row); err != nil {
		b.log.Printf("Correo: marcando enviado id %d: %v", id, err)
	}
	b.correoRegistrarIntento(id, intentos+1, "ok", 250, "")
}

func (b *Bot) correoMarcarReintento(id, intentos int64, res correo.Resultado, espera time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	row := map[string]any{
		"estado_envio":    "pendiente",
		"intentos":        intentos + 1,
		"proximo_intento": time.Now().Add(espera).UTC().Format(time.RFC3339),
		"error_envio":     res.Err.Error(),
		"locked_at":       nil,
		"locked_by":       nil,
		"updated_at":      time.Now().UTC().Format(time.RFC3339),
	}
	if err := b.supabaseUpdate(ctx, tablaCorreos, "?id=eq."+strconv.FormatInt(id, 10), row); err != nil {
		b.log.Printf("Correo: marcando reintento id %d: %v", id, err)
	}
	b.correoRegistrarIntento(id, intentos+1, "error", res.Codigo, res.Err.Error())
}

func (b *Bot) correoErrorDefinitivo(id int64, tipo string, intentos int64, detalle string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	row := map[string]any{
		"estado_envio": "error",
		"intentos":     intentos + 1,
		"error_envio":  detalle,
		"locked_at":    nil,
		"locked_by":    nil,
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
	}
	if err := b.supabaseUpdate(ctx, tablaCorreos, "?id=eq."+strconv.FormatInt(id, 10), row); err != nil {
		b.log.Printf("Correo: marcando error id %d: %v", id, err)
	}
	b.correoRegistrarIntento(id, intentos+1, "error", 0, detalle)

	// Anti-bucle: si el que falló es el correo de alerta, avisar por WhatsApp
	// al número fijo; ese WhatsApp NO genera otra alerta.
	if tipo == "error_reenvio" {
		b.correoFallbackWhatsApp()
	}
}

// correoFallbackWhatsApp avisa por WhatsApp que el correo de alerta no pudo
// enviarse. Es el último recurso (luego habrá un bot de Telegram).
func (b *Bot) correoFallbackWhatsApp() {
	if b.simEnabled() {
		return
	}
	b.mu.Lock()
	cli := b.client
	b.mu.Unlock()
	if cli == nil {
		return
	}
	b.sendText(jidFor(fallbackWA),
		"⚠️ No pude enviar el correo de alerta desde "+alertaRemitente+
			". Revisa la configuración del sistema de correo.")
}

func (b *Bot) correoRegistrarIntento(correoID, intento int64, resultado string, codigo int, detalle string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	row := map[string]any{
		"correo_id":   correoID,
		"intento":     intento,
		"resultado":   resultado,
		"codigo_smtp": codigo,
		"detalle":     detalle,
	}
	if err := b.supa.Insert(ctx, tablaIntentos, row); err != nil {
		b.log.Printf("Correo: registrando intento de %d: %v", correoID, err)
	}
}

// supabaseUpdate hace PATCH con reintentos (contexto propio), para que el
// marcado no se pierda si la red/BD tiene hipos. NO agrega columnas: cada
// llamador incluye las que su tabla tenga (p. ej. `updated_at`).
func (b *Bot) supabaseUpdate(ctx context.Context, table, filter string, row map[string]any) error {
	const attempts = 3
	var err error
	for i := 1; i <= attempts; i++ {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = b.supa.Update(c, table, filter, row)
		cancel()
		if err == nil {
			return nil
		}
		if i < attempts {
			time.Sleep(time.Duration(i) * 2 * time.Second)
		}
	}
	return err
}

// correoEsHora indica si ya llegó el momento de enviar según fecha/hora
// programada (hora Venezuela). Sin fecha programada, se envía de inmediato.
func correoEsHora(r map[string]any) bool {
	fp := strings.TrimSpace(supabase.GetString(r, "fecha_programada"))
	if fp == "" {
		return true
	}
	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	hoy := now.Format("2006-01-02")
	if fp > hoy {
		return false
	}
	if fp < hoy {
		return true
	}
	hp := strings.TrimSpace(supabase.GetString(r, "hora_programada"))
	if hp == "" {
		return true
	}
	return now.Format("15:04:05") >= hp
}

// registrarErrorWhatsApp guarda un fallo de envío de WhatsApp de campaña
// (cobranzas / programados) para que el digest de alertas lo notifique.
func (b *Bot) registrarErrorWhatsApp(origen, numero, texto, detalle string) {
	if !cfgCorreo.enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	row := map[string]any{
		"origen":         origen,
		"numero_destino": numero,
		"texto":          texto,
		"error":          detalle,
	}
	if err := b.supa.Insert(ctx, tablaErrores, row); err != nil {
		b.log.Printf("Correo: registrando error WhatsApp (%s): %v", origen, err)
	}
}

// ---------------------------------------------------------------------------
// Digest de alertas
// ---------------------------------------------------------------------------

func (b *Bot) correoAlertasWorker() {
	interval := time.Duration(cfgCorreo.alertasSecs) * time.Second
	for {
		time.Sleep(interval)
		if b.simEnabled() {
			continue
		}
		b.correoEnviarDigest()
	}
}

// correoEnviarDigest agrupa los fallos de WhatsApp pendientes de notificar en
// UN correo (tipo error_reenvio) desde el remitente fijo.
func (b *Bot) correoEnviarDigest() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := b.supa.Select(ctx, tablaErrores,
		"?select=id,socio_comercial,origen,numero_destino,texto,error,fecha"+
			"&notificado=eq.false&order=fecha.asc&limit=200")
	if err != nil {
		b.log.Printf("Correo alertas: leyendo errores: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}

	rem, ok, err := b.supa.SelectOne(ctx, tablaCuentas,
		"?select=id&email=eq."+url.QueryEscape(alertaRemitente)+"&activo=eq.true&limit=1")
	if err != nil || !ok {
		b.log.Printf("Correo alertas: remitente %s no configurado (%v)", alertaRemitente, err)
		return
	}
	remitenteID := supabase.GetInt(rem, "id")
	socio := supabase.GetInt(rows[0], "socio_comercial")

	asunto := fmt.Sprintf("[Bot] %d fallo(s) de envío por WhatsApp", len(rows))
	bodyHTML, bodyTexto := correoDigestCuerpo(rows)

	emailRow := map[string]any{
		"socio_comercial": socio,
		"remitente_id":    remitenteID,
		"para":            alertaPara,
		"asunto":          asunto,
		"cuerpo_html":     bodyHTML,
		"cuerpo_texto":    bodyTexto,
		"tipo":            "error_reenvio",
		"referencia":      "alertas-wa", // fixed: el índice único serializa digests
		"prioridad":       1,
	}
	// Si ya hay un digest pendiente/enviando, el índice único rechaza (409):
	// se reintenta en el siguiente ciclo, cuando el anterior haya salido.
	if err := b.supa.Insert(ctx, tablaCorreos, emailRow); err != nil {
		if !strings.Contains(err.Error(), "409") {
			b.log.Printf("Correo alertas: insertando digest: %v", err)
		}
		return
	}

	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = strconv.FormatInt(supabase.GetInt(r, "id"), 10)
	}
	filter := "?id=in.(" + strings.Join(ids, ",") + ")"
	if err := b.supabaseUpdate(ctx, tablaErrores, filter, map[string]any{"notificado": true}); err != nil {
		b.log.Printf("Correo alertas: marcando notificados: %v", err)
		return
	}
	b.log.Printf("Correo alertas: digest con %d fallo(s) encolado", len(rows))
}

// correoDigestCuerpo arma el HTML y el texto del digest.
func correoDigestCuerpo(rows []map[string]any) (bodyHTML, bodyTexto string) {
	var hb, tb strings.Builder
	hb.WriteString("<h2>Fallos de envío por WhatsApp</h2>")
	hb.WriteString("<p>Se detectaron los siguientes fallos:</p>")
	hb.WriteString(`<table border="1" cellpadding="6" cellspacing="0">`)
	hb.WriteString("<tr><th>Fecha</th><th>Origen</th><th>Número</th><th>Texto</th><th>Error</th></tr>")
	tb.WriteString("Fallos de envío por WhatsApp:\n\n")
	for _, r := range rows {
		f := supabase.GetString(r, "fecha")
		o := supabase.GetString(r, "origen")
		n := supabase.GetString(r, "numero_destino")
		t := supabase.GetString(r, "texto")
		e := supabase.GetString(r, "error")
		fmt.Fprintf(&hb, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>",
			htmlesc.EscapeString(f), htmlesc.EscapeString(o), htmlesc.EscapeString(n),
			htmlesc.EscapeString(t), htmlesc.EscapeString(e))
		fmt.Fprintf(&tb, "- [%s] %s | %s | %s | %s\n", f, o, n, t, e)
	}
	hb.WriteString("</table>")
	return hb.String(), tb.String()
}

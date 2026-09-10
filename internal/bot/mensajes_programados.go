package bot

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"time"

	"bot/internal/supabase"
)

// Mensajes programados (broadcast personalizado del CRM).
//
// El administrador agenda mensajes a números determinados en una fecha/hora
// futura desde la UI (/mensajes/programar). El bot actúa como consumidor:
// lee las filas 'pendiente' cuya fecha_programada ya llegó (<= hoy local) y
// cuya hora_programada ya pasó, envía el `texto` al `numero_destino` por
// WhatsApp (anti-baneo) y marca enviado/error.
//
// NO usa la edge function notificar-cobranzas: lee la tabla directamente.
// El número se normaliza igual que cobranzas (0424... -> 58424...).
//
// Env:
//   - MENSAJES_PROGRAMADOS_ENABLED (default "true")
//   - MENSAJES_PROGRAMADOS_POLL_SECS (default 120)

const mensajesTable = "mensajes_programados"

func mensajesConfig() (enabled bool, pollSecs int) {
	enabled = os.Getenv("MENSAJES_PROGRAMADOS_ENABLED") != "false"
	pollSecs = 120
	if v, err := strconv.Atoi(os.Getenv("MENSAJES_PROGRAMADOS_POLL_SECS")); err == nil && v >= 30 {
		pollSecs = v
	}
	return enabled, pollSecs
}

// startMensajesProgramados arranca el consumidor de mensajes programados.
func (b *Bot) startMensajesProgramados() {
	enabled, poll := mensajesConfig()
	if !enabled {
		b.log.Printf("Mensajes programados deshabilitado (MENSAJES_PROGRAMADOS_ENABLED=false)")
		return
	}
	b.log.Printf("Mensajes programados activo (poll cada %ds)", poll)
	go b.mensajesConsumidor(poll)
}

// mensajesConsumidor revisa periódicamente los pendientes con fecha/hora ya
// llegada y los envía.
func (b *Bot) mensajesConsumidor(pollSecs int) {
	interval := time.Duration(pollSecs) * time.Second
	for {
		time.Sleep(interval)
		if b.simEnabled() || b.client == nil {
			continue
		}
		b.mensajesProcesarPendientes()
	}
}

// mensajesProcesarPendientes lee y envía los mensajes programados cuya hora
// ya llegó.
func (b *Bot) mensajesProcesarPendientes() {
	selCtx, selCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer selCancel()

	// Hora local Venezuela para filtrar lo que ya debe enviarse.
	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	hoy := now.Format("2006-01-02")
	ahora := now.Format("15:04:05")

	query := "?select=id,texto,numero_destino,nombre_destino,estado_envio,intentos" +
		"&estado_envio=eq.pendiente" +
		"&fecha_programada=lte." + url.QueryEscape(hoy) +
		"&or=(hora_programada.lte." + url.QueryEscape(ahora) + ",hora_programada.is.null)" +
		"&limit=50"
	rows, err := b.supa.Select(selCtx, mensajesTable, query)
	if err != nil {
		b.log.Printf("Mensajes programados: leyendo pendientes: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	b.log.Printf("Mensajes programados: %d mensajes pendientes por enviar", len(rows))

	for _, r := range rows {
		id := supabase.GetInt(r, "id")
		texto := supabase.GetString(r, "texto")
		numero := supabase.GetString(r, "numero_destino")
		intentos := supabase.GetInt(r, "intentos")
		if id == 0 || texto == "" || numero == "" {
			b.log.Printf("Mensajes programados: fila inválida id=%d (texto/numero vacío)", id)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		sendErr := b.sendCobranzas(ctx, jidFor(normalizeWaNumber(numero)), texto)
		estado := "enviado"
		detalle := ""
		if sendErr != nil {
			estado = "error"
			detalle = sendErr.Error()
		}
		b.marcarEnvio(ctx, mensajesTable, id, estado, detalle, intentos)
		cancel()
	}
}

package bot

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/supabase"
)

// Sistema de recordatorios de cobranzas por WhatsApp.
//
// El bot actúa como reloj + consumidor de la cola `cola_mensajes_cobranzas`
// del CRM (ver AGENTS.md del proyecto "capital - cotizaciones"):
//
//	1) RELOJ: 1 vez al día (hora Venezuela, env COBRANZAS_HOUR) dispara la
//	   Edge Function `notificar-cobranzas` con el header `x-cron-auth`. La
//	   función llama a la RPC `cobranzas_encolar_mensajes()` (service_role)
//	   que construye los textos y los encola con estado 'pendiente'.
//	2) CONSUMIDOR: cada COBRANZAS_POLL_SECS lee los pendientes de
//	   `cola_mensajes_cobranzas`, envía el `texto` al `numero_destino` por
//	   WhatsApp (respetando el anti-baneo del Guard) y marca la fila como
//	   'enviado' o 'error' (+1 intento).
//
// El bot NO construye ni decide mensajes: solo dispara el reloj y consume.
// En modo simulación se omite todo (no tocar la cola real).
//
// Env:
//   - COBRANZAS_ENABLED  (default "true"; "false" desactiva todo)
//   - COBRANZAS_CRON_AUTH  secreto x-cron-auth de la Edge Function
//   - COBRANZAS_HOUR     hora del día (Venezuela) para el reloj (default 8)
//   - COBRANZAS_POLL_SECS intervalo del consumidor en segundos (default 120)

const (
	cobranzasTable    = "cola_mensajes_cobranzas"
	cobranzasFunction = "notificar-cobranzas"
)

// cobranzasConfig agrupa la configuración del sistema de cobranzas.
type cobranzasConfig struct {
	enabled  bool
	cronAuth string
	hour     int
	pollSecs int
}

func cobranzasFromEnv() cobranzasConfig {
	c := cobranzasConfig{
		enabled:  os.Getenv("COBRANZAS_ENABLED") != "false",
		cronAuth: os.Getenv("COBRANZAS_CRON_AUTH"),
		hour:     8,
		pollSecs: 120,
	}
	if v, err := strconv.Atoi(os.Getenv("COBRANZAS_HOUR")); err == nil && v >= 0 && v < 24 {
		c.hour = v
	}
	if v, err := strconv.Atoi(os.Getenv("COBRANZAS_POLL_SECS")); err == nil && v >= 30 {
		c.pollSecs = v
	}
	return c
}

// startCobranzas arranca los workers de cobranzas (reloj + consumidor).
// Se llama desde Bot.New; no requiere cliente conectado para arrancar
// (los loops se protegen con simEnabled/client==nil).
func (b *Bot) startCobranzas() {
	cfg := cobranzasFromEnv()
	if !cfg.enabled {
		b.log.Printf("Cobranzas deshabilitado (COBRANZAS_ENABLED=false)")
		return
	}
	b.log.Printf("Cobranzas activo (reloj %02d:00 hora VE, poll cada %ds, cron_auth=%s)",
		cfg.hour, cfg.pollSecs, maskToken(cfg.cronAuth))
	go b.cobranzasReloj(cfg)
	go b.cobranzasConsumidor(cfg)
}

// cobranzasReloj dispara la edge function una vez por día (hora Venezuela).
// Idempotente: la RPC no duplica por (cuota, tipo, fecha).
func (b *Bot) cobranzasReloj(cfg cobranzasConfig) {
	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.Local
	}
	last := ""
	for {
		// No disparar en simulación/probe: no poblar la cola real.
		if !b.simEnabled() && b.client != nil {
			now := time.Now().In(loc)
			if now.Format("2006-01-02") != last && now.Hour() >= cfg.hour {
				last = now.Format("2006-01-02")
				b.cobranzasDispararReloj(cfg)
			}
		}
		time.Sleep(30 * time.Minute)
	}
}

// cobranzasDispararReloj llama a la Edge Function notificar-cobranzas.
func (b *Bot) cobranzasDispararReloj(cfg cobranzasConfig) {
	if cfg.cronAuth == "" {
		b.log.Printf("Cobranzas: COBRANZAS_CRON_AUTH vacío; reloj no disparado")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var resp map[string]any
	err := b.supa.EdgeFunctionWithHeaders(ctx, cobranzasFunction, map[string]any{},
		&resp, map[string]string{"x-cron-auth": cfg.cronAuth})
	if err != nil {
		b.log.Printf("Cobranzas reloj: %v", err)
		return
	}
	if e, ok := resp["error"].(string); ok && e != "" {
		b.log.Printf("Cobranzas reloj: error de la función: %s", e)
		return
	}
	b.log.Printf("Cobranzas reloj: %v", resp["mensaje"])
}

// cobranzasConsumidor lee los pendientes y los envía por WhatsApp.
func (b *Bot) cobranzasConsumidor(cfg cobranzasConfig) {
	interval := time.Duration(cfg.pollSecs) * time.Second
	for {
		time.Sleep(interval)
		if b.simEnabled() || b.client == nil {
			continue
		}
		b.cobranzasProcesarPendientes()
	}
}

// cobranzasProcesarPendientes envía los mensajes pendientes de la cola.
func (b *Bot) cobranzasProcesarPendientes() {
	selCtx, selCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer selCancel()

	rows, err := b.supa.Select(selCtx, cobranzasTable,
		"?select=id,texto,numero_destino,nombre_destino,tipo_mensaje,estado_envio,intentos"+
			"&estado_envio=eq.pendiente&limit=50")
	if err != nil {
		b.log.Printf("Cobranzas: leyendo pendientes: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	b.log.Printf("Cobranzas: %d mensajes pendientes por enviar", len(rows))

	for _, r := range rows {
		id := supabase.GetInt(r, "id")
		texto := supabase.GetString(r, "texto")
		numero := supabase.GetString(r, "numero_destino")
		intentos := supabase.GetInt(r, "intentos")
		if id == 0 || texto == "" || numero == "" {
			b.log.Printf("Cobranzas: fila inválida id=%d (texto/numero vacío)", id)
			continue
		}
		// Contexto propio por mensaje: un lote grande no debe expirar el
		// contexto global a mitad (el gap anti-baneo se acumula).
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		sendErr := b.sendCobranzas(ctx, jidFor(normalizeWaNumber(numero)), texto)
		estado := "enviado"
		detalle := ""
		if sendErr != nil {
			estado = "error"
			detalle = sendErr.Error()
		}
		b.marcarCobranza(ctx, id, estado, detalle, intentos)
		cancel()
	}
}

// sendCobranzas envía un mensaje de texto por WhatsApp respetando el
// anti-baneo (gap + cuota), igual que el outbox pero de forma síncrona.
func (b *Bot) sendCobranzas(ctx context.Context, to types.JID, text string) error {
	if err := b.guard.checkQuota(to.User); err != nil {
		return err
	}
	if err := b.guard.waitGap(ctx); err != nil {
		return err
	}
	if err := b.sendTextDirect(ctx, to, text); err != nil {
		return err
	}
	b.guard.markSent(to.User)
	b.log.Printf("Cobranzas enviado a %s: %s", to.User, text)
	return nil
}

// marcarCobranza actualiza el estado de envío de una fila de la cola.
func (b *Bot) marcarCobranza(ctx context.Context, id int64, estado, detalle string, intentos int64) {
	row := map[string]any{"estado_envio": estado}
	if estado == "enviado" {
		row["fecha_envio"] = time.Now().UTC().Format(time.RFC3339)
		row["error_envio"] = nil
	} else {
		row["error_envio"] = detalle
		row["intentos"] = intentos + 1
	}
	filter := "?id=eq." + url.QueryEscape(strconv.FormatInt(id, 10))
	if err := b.supa.Update(ctx, cobranzasTable, filter, row); err != nil {
		b.log.Printf("Cobranzas: marcando id %d como %s: %v", id, estado, err)
	}
}

// normalizeWaNumber normaliza un número de WhatsApp a dígitos con código de
// país. Elimina +, espacios y guiones. Si el número viene en formato local
// venezolano (11 dígitos empezando en 0, ej: "04248821071"), lo convierte a
// internacional ("584248821071").
func normalizeWaNumber(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 11 && d[0] == '0' {
		return "58" + d[1:]
	}
	return d
}

// maskToken oculta un secreto para los logs (primeros 2 + últimos 2 chars).
func maskToken(s string) string {
	if len(s) <= 4 {
		return "***"
	}
	return s[:2] + "***" + s[len(s)-2:]
}

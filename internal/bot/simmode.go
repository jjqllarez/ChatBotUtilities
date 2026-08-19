package bot

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"bot/internal/supabase"
)

// Modo simulación: cuando bot_config.simulation = 'true' (o se fuerza con
// ForceSim) el bot procesa los mensajes por el pipeline completo (ruteo,
// flujos, LLM, Supabase, PDF) pero NO envía nada por WhatsApp: registra en
// consola lo que habría enviado y guarda los adjuntos en sim_out/. Así se
// puede probar lógica de ruteo y negocio sin riesgo de baneo.
//
// El flag se lee de Supabase con caché de simModeTTL para que el cambio aplique
// en caliente (sin reiniciar) y sin pegarle a PostgREST en cada mensaje.

const simModeTTL = 10 * time.Second

type simState struct {
	mu      sync.Mutex
	enabled bool
	checked time.Time
	forced  bool // true => no consultar Supabase, sim siempre activo
}

// simEnabled devuelve true si el modo simulación está activo (forzado o por
// flag en Supabase). Si la lectura falla (red/404) conserva el último valor
// conocido para no bloquear la cola de salida.
func (b *Bot) simEnabled() bool {
	b.sim.mu.Lock()
	defer b.sim.mu.Unlock()

	if b.sim.forced {
		return true
	}
	if time.Since(b.sim.checked) < simModeTTL {
		return b.sim.enabled
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	val, err := b.readSimFlag(ctx)
	if err == nil {
		b.sim.enabled = val
	}
	b.sim.checked = time.Now()
	return b.sim.enabled
}

// ForceSim activa/desactiva el modo simulación sin depender del flag de
// Supabase. Lo usa el driver de pruebas (cmd/simchat) que no tiene cliente.
func (b *Bot) ForceSim(on bool) {
	b.sim.mu.Lock()
	defer b.sim.mu.Unlock()
	b.sim.forced = on
	b.sim.enabled = on
	// Evitar re-consultar Supabase durante la sesión forzada.
	b.sim.checked = time.Now().Add(simModeTTL)
}

// readSimFlag consulta bot_config.simulation (value = 'true'/'false').
func (b *Bot) readSimFlag(ctx context.Context) (bool, error) {
	row, ok, err := b.supa.SelectOne(ctx, "bot_config",
		"?select=value&key=eq."+url.QueryEscape("simulation"))
	if err != nil {
		return false, fmt.Errorf("bot_config: %w", err)
	}
	if !ok {
		return false, nil
	}
	return supabase.GetString(row, "value") == "true", nil
}

// simEmit registra en consola lo que el bot habría enviado y guarda los
// adjuntos en sim_out/ (no toca whatsmeow: cero tráfico a WhatsApp).
func (b *Bot) simEmit(m outMsg) {
	if m.media != nil {
		name := m.filename
		if name == "" {
			name = "adjunto"
		}
		dir := "sim_out"
		_ = os.MkdirAll(dir, 0o755)
		path := filepath.Join(dir, fmt.Sprintf("%d_%s", time.Now().UnixMilli(), filepath.Base(name)))
		if werr := os.WriteFile(path, m.media.data, 0o644); werr != nil {
			b.log.Printf("[SIM] guardando adjunto %s: %v", path, werr)
			return
		}
		b.log.Printf("[SIM] -> %s adjunto %q (%d bytes) guardado en %s", m.jid.User, name, len(m.media.data), path)
		return
	}
	if m.text != "" {
		b.log.Printf("[SIM] -> %s: %s", m.jid.User, m.text)
	}
}

// SimulateMessage inyecta un mensaje de texto como si viniera del teléfono
// `phone` por el pipeline real (handleMessageV2). No toca whatsmeow: el
// driver de pruebas (cmd/simchat) la usa para validar ruteo/negocio.
func (b *Bot) SimulateMessage(phone, text string) {
	jid := types.NewJID(phone, types.DefaultUserServer)
	ev := &events.Message{
		Info: types.MessageInfo{
			ID:        "SIM-" + strconv.FormatInt(time.Now().UnixNano(), 10),
			Timestamp: time.Now(),
			MessageSource: types.MessageSource{
				Sender: jid,
				Chat:   jid,
			},
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
	b.handleMessageV2(ev)
}

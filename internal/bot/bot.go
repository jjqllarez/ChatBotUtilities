package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"bot/internal/empleados"
	"bot/internal/llm"
	"bot/internal/supabase"
)

// DeviceContainer es el subconjunto del container de whatsmeow que el bot usa.
// Lo implementan tanto el container Supabase como sqlstore (SQLite local).
type DeviceContainer interface {
	GetAllDevices(ctx context.Context) ([]*store.Device, error)
	NewDevice() *store.Device
	DeleteDevice(ctx context.Context, device *store.Device) error
}

// Bot es el orquestador: conexión whatsmeow, evento de mensajes, cola de salida
// con anti-baneo, flujo de cotización y asistente LLM.
type Bot struct {
	container DeviceContainer
	supa      *supabase.Client
	qrPort    string
	llmClient *llm.Client
	log       *log.Logger

	mu     sync.Mutex
	client *whatsmeow.Client // re-created en re-vinculación

	guard   *Guard
	flows   *flowManager
	history *HistoryStore
	state   *StateStore

	adminCacheMu sync.Mutex
	adminCache   map[string]struct{}

	qrMu     sync.Mutex
	latestQR string

	reconnect chan struct{}
	out       chan outMsg
	stop      chan struct{}
}

// outMsg es un mensaje de salida en cola (texto o adjunto).
type outMsg struct {
	jid     types.JID
	text    string
	media   *mediaPayload
	mime    string
	filename string
}

// mediaPayload describe un adjunto a enviar.
type mediaPayload struct {
	data []byte
	kind string // "image" | "document" | "audio"
}

// New crea el bot: carga el dispositivo guardado (o crea uno nuevo), registra
// los handlers y levanta el servidor HTTP del QR.
func New(container DeviceContainer, supa *supabase.Client, qrPort string, llmClient *llm.Client) (*Bot, error) {
	if qrPort == "" {
		qrPort = "8080"
	}
	logger := log.New(os.Stderr, "[bot] ", log.LstdFlags)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	devices, err := container.GetAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("cargando dispositivos: %w", err)
	}
	var device *store.Device
	if len(devices) > 0 {
		device = devices[0]
		logger.Printf("Sesión recuperada: %s", device.ID)
	} else {
		device = container.NewDevice()
		logger.Printf("Sin sesión guardada; se generará un QR para vincular")
	}

	b := &Bot{
		container:  container,
		supa:       supa,
		qrPort:     qrPort,
		llmClient:  llmClient,
		log:        logger,
		guard:      newGuard(),
		adminCache: make(map[string]struct{}),
		out:        make(chan outMsg, 64),
		stop:       make(chan struct{}),
		reconnect:  make(chan struct{}, 1),
	}
	b.history = NewHistoryStore(supa)
	b.state = NewStateStore(supa)
	b.flows = newFlowManager(supa, b)

	b.initClient(device)
	go b.outWorker()
	go b.startQRServer()
	go b.dailyCleanup()
	return b, nil
}

func (b *Bot) initClient(device *store.Device) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.client = whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "INFO", false))
	b.client.AddEventHandler(b.eventHandler)
}

// eventHandler despacha los eventos de whatsmeow.
func (b *Bot) eventHandler(ev interface{}) {
	switch v := ev.(type) {
	case *events.Message:
		go b.handleMessage(v)
	case *events.QR:
		b.displayQR(v.Codes)
	case *events.LoggedOut:
		b.log.Printf("Sesión cerrada desde WhatsApp; se generará un QR nuevo")
		go b.onLoggedOut()
	case *events.PairSuccess:
		b.log.Printf("Vinculación exitosa")
	case *events.Connected:
		b.log.Printf("Conectado a WhatsApp")
	case *events.Disconnected:
		b.log.Printf("Desconectado de WhatsApp")
		b.requestReconnect()
	case *events.ConnectFailure:
		b.log.Printf("Fallo de conexión: %v %s", v.Reason, v.Message)
		b.requestReconnect()
	case *events.StreamError:
		b.log.Printf("Error de stream (código %s)", v.Code)
		b.requestReconnect()
	case *events.TemporaryBan:
		b.log.Printf("Baneo temporal: %v", v)
	default:
	}
}

// displayQR imprime el QR en la terminal y lo guarda para el servidor HTTP.
func (b *Bot) displayQR(codes []string) {
	if len(codes) == 0 {
		return
	}
	code := codes[0]
	b.qrMu.Lock()
	b.latestQR = code
	b.qrMu.Unlock()

	b.log.Printf("Escanea el QR con WhatsApp -> Dispositivos vinculados")
	fmt.Printf("\n   QR: http://<servidor>:%s/qr\n", b.qrPort)
	qrterminal.GenerateHalfBlock(code, qrterminal.L, os.Stdout)
}

// onLoggedOut borra el dispositivo, crea uno nuevo y fuerza la reconexión.
func (b *Bot) onLoggedOut() {
	b.mu.Lock()
	defer b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if b.client.Store != nil && b.client.Store.ID != nil {
		_ = b.container.DeleteDevice(ctx, b.client.Store)
	}
	b.client.Disconnect()

	device := b.container.NewDevice()
	b.client = whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "INFO", false))
	b.client.AddEventHandler(b.eventHandler)
	b.requestReconnect()
}

// requestReconnect avisa al bucle de Run para volver a conectar.
func (b *Bot) requestReconnect() {
	select {
	case b.reconnect <- struct{}{}:
	default:
	}
}

// Run conecta y mantiene el bot activo hasta que se cierre stop.
func (b *Bot) Run(stop <-chan struct{}) error {
	for {
		b.mu.Lock()
		err := b.client.Connect()
		b.mu.Unlock()
		if err != nil {
			b.log.Printf("Error conectando: %v (reintento en 5s)", err)
			select {
			case <-stop:
				return nil
			case <-time.After(5 * time.Second):
				continue
			}
		}

		select {
		case <-stop:
			b.client.Disconnect()
			return nil
		case <-b.reconnect:
			b.mu.Lock()
			b.client.Disconnect()
			b.mu.Unlock()
			// Pequeña pausa antes de reconectar para no parecer comportamiento
			// automatizado (anti-baneo).
			select {
			case <-stop:
				return nil
			case <-time.After(3 * time.Second):
			}
		}
	}
}

// handleMessage despacha el mensaje según la versión activa (flag BOT_V2).
func (b *Bot) handleMessage(ev *events.Message) {
	if os.Getenv("BOT_V2") == "true" {
		b.handleMessageV2(ev)
		return
	}
	b.handleMessageLegacy(ev)
}

// handleMessageLegacy es el pipeline v1: router intent.go + LLM con tools.
func (b *Bot) handleMessageLegacy(ev *events.Message) {
	if ev.Info.IsFromMe || ev.Info.IsGroup {
		return
	}
	msg := ev.Message
	if msg == nil || ev.Info.Sender.IsEmpty() {
		return
	}
	phone := ev.Info.Sender.ToNonAD().User
	// Si el remitente es un LID, el número real está en SenderAlt
	if alt := ev.Info.SenderAlt.ToNonAD().User; alt != "" {
		phone = alt
	}
	if phone == "" {
		b.log.Printf("DEBUG msg %s sin phone (sender=%s senderAlt=%s chat=%s)",
			ev.Info.ID, ev.Info.Sender.String(), ev.Info.SenderAlt.String(), ev.Info.Chat.String())
		return
	}
	b.log.Printf("DEBUG msg %s phone=%s sender=%s senderAlt=%s chat=%s",
		ev.Info.ID, phone, ev.Info.Sender.String(), ev.Info.SenderAlt.String(), ev.Info.Chat.String())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Marcar leído.
	_ = b.client.MarkRead(ctx, []types.MessageID{ev.Info.ID}, ev.Info.Timestamp, ev.Info.Chat, ev.Info.Sender)

	emp, err := empleados.LookupByPhone(ctx, b.supa, phone)
	if err != nil {
		b.log.Printf("Error buscando empleado %s: %v", phone, err)
		return
	}
	// Silencioso si no es un empleado activo.
	if emp == nil {
		b.log.Printf("DEBUG %s no es empleado activo (telefono=%s)", phone, phone)
		return
	}

	text := messageText(msg)
	// Notas de voz: transcribir y usar como texto.
	if msg.GetAudioMessage() != nil {
		if b.llmClient == nil {
			return
		}
		audio, derr := b.client.Download(ctx, msg.GetAudioMessage())
		if derr != nil {
			b.log.Printf("Descargando audio de %s: %v", phone, derr)
			return
		}
		tr, terr := b.llmClient.Transcribe(ctx, os.Getenv("TRANSCRIBE_MODEL"), audio, "ogg")
		if terr != nil {
			b.log.Printf("Transcribiendo audio de %s: %v", phone, terr)
			return
		}
		text = strings.TrimSpace(tr)
		if text == "" {
			return
		}
	}

	if text == "" {
		return
	}

	// Guardar en memoria (historial LLM).
	_ = b.history.Append(ctx, phone, "user", text)

	if b.flows.handleCommand(phone, emp, text) {
		return
	}
	if b.handleDirectIntent(ctx, ev.Info.Chat, phone, emp, text) {
		return
	}
	if b.flows.active(ctx, phone) {
		err := b.flows.process(ctx, phone, emp, text)
		if err == errNeedsAssistant {
			// La interrupción no pertenece al flujo en curso: el asistente la
			// atiende (ficha, catálogo, otra petición) y el borrador se conserva.
			if b.llmClient == nil {
				b.sendText(ev.Info.Chat, "Comandos: /cotizar, /listar, /cancelar.")
				return
			}
			b.runAssistant(ctx, ev.Info.Chat, phone, emp, text, b.flows.stepHint(ctx, phone))
			return
		}
		if err != nil && err != errNoFlow {
			b.log.Printf("Flujo %s: %v", phone, err)
		}
		return
	}
	if b.llmClient == nil {
		b.sendText(ev.Info.Chat, "Hola "+firstWord(emp.NombreCompleto)+", actualmente estoy en mantenimiento. Comandos: /cotizar, /listar, /cancelar.")
		return
	}
	b.runAssistant(ctx, ev.Info.Chat, phone, emp, text, "")
}

// handleMessageV2 es el pipeline v2 (flag BOT_V2): router determinista
// (classifyIntent) para todo el negocio y LLM solo para conversación.
func (b *Bot) handleMessageV2(ev *events.Message) {
	if ev.Info.IsFromMe || ev.Info.IsGroup {
		return
	}
	msg := ev.Message
	if msg == nil || ev.Info.Sender.IsEmpty() {
		return
	}
	phone := ev.Info.Sender.ToNonAD().User
	if alt := ev.Info.SenderAlt.ToNonAD().User; alt != "" {
		phone = alt
	}
	if phone == "" {
		b.log.Printf("DEBUG msg %s sin phone (sender=%s senderAlt=%s chat=%s)",
			ev.Info.ID, ev.Info.Sender.String(), ev.Info.SenderAlt.String(), ev.Info.Chat.String())
		return
	}
	b.log.Printf("DEBUG msg %s phone=%s sender=%s senderAlt=%s chat=%s",
		ev.Info.ID, phone, ev.Info.Sender.String(), ev.Info.SenderAlt.String(), ev.Info.Chat.String())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_ = b.client.MarkRead(ctx, []types.MessageID{ev.Info.ID}, ev.Info.Timestamp, ev.Info.Chat, ev.Info.Sender)

	emp, err := empleados.LookupByPhone(ctx, b.supa, phone)
	if err != nil {
		b.log.Printf("Error buscando empleado %s: %v", phone, err)
		return
	}
	if emp == nil {
		b.log.Printf("DEBUG %s no es empleado activo (telefono=%s)", phone, phone)
		return
	}

	text := messageText(msg)
	if msg.GetAudioMessage() != nil {
		if b.llmClient == nil {
			return
		}
		audio, derr := b.client.Download(ctx, msg.GetAudioMessage())
		if derr != nil {
			b.log.Printf("Descargando audio de %s: %v", phone, derr)
			return
		}
		tr, terr := b.llmClient.Transcribe(ctx, os.Getenv("TRANSCRIBE_MODEL"), audio, "ogg")
		if terr != nil {
			b.log.Printf("Transcribiendo audio de %s: %v", phone, terr)
			return
		}
		text = strings.TrimSpace(tr)
		if text == "" {
			return
		}
	}
	if text == "" {
		return
	}

	_ = b.history.Append(ctx, phone, "user", text)

	if b.flows.handleCommand(phone, emp, text) {
		return
	}
	flowActive := b.flows.active(ctx, phone)
	// Selección pendiente de ficha (solo cuando no hay flujo /cotizar activo).
	if !flowActive && b.handleFichaPick(ctx, ev.Info.Chat, phone, emp, text) {
		return
	}
	if flowActive {
		err := b.flows.process(ctx, phone, emp, text)
		if err == errNeedsAssistant {
			// Interrupción del flujo: primero el router determinista (ficha,
			// imprimir, catálogo, listar); si no es negocio, conversación.
			if b.handleRouterIntent(ctx, ev.Info.Chat, phone, emp, text) {
				return
			}
			if b.llmClient == nil {
				b.sendText(ev.Info.Chat, "Comandos: /cotizar, /listar, /cancelar.")
				return
			}
			b.runAssistantV2(ctx, ev.Info.Chat, phone, emp, text, b.flows.stepHint(ctx, phone))
			return
		}
		if err != nil && err != errNoFlow {
			b.log.Printf("Flujo %s: %v", phone, err)
		}
		return
	}
	if b.handleRouterIntent(ctx, ev.Info.Chat, phone, emp, text) {
		return
	}
	if b.llmClient == nil {
		b.sendText(ev.Info.Chat, "Hola "+firstWord(emp.NombreCompleto)+", actualmente estoy en mantenimiento. Comandos: /cotizar, /listar, /cancelar.")
		return
	}
	b.runAssistantV2(ctx, ev.Info.Chat, phone, emp, text, "")
}

// runAssistantV2 usa el LLM solo para conversación: el router determinista ya
// se encarga de ficha, catálogo, imprimir y listar (sin tools de negocio).
func (b *Bot) runAssistantV2(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string, flowHint string) {
	hist, err := b.history.Recent(ctx, phone, 40)
	if err != nil {
		hist = nil
	}
	if len(hist) == 0 {
		b.sendText(chat, greetingFor(emp))
	}

	messages := []llm.Message{
		{Role: "system", Content: buildConversationPrompt()},
	}
	if flowHint != "" {
		messages = append(messages, llm.Message{Role: "system", Content: flowHint})
	}
	for _, h := range hist {
		messages = append(messages, llm.Message{Role: h.Role, Content: h.Content})
	}
	messages = append(messages, llm.Message{Role: "user", Content: text})

	llmCtx, llmCancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer llmCancel()
	resp, err := b.llmClient.Chat(llmCtx, messages, nil)
	if err != nil {
		b.log.Printf("LLM %s: %v", phone, err)
		b.sendText(chat, "Estoy teniendo problemas de conexión con el asistente. Intenta de nuevo en un momento.")
		return
	}
	if resp.Content != "" {
		b.sendText(chat, resp.Content)
	}
}

// buildConversationPrompt arma el prompt del asistente v2: solo conversación y
// aclaraciones, sin tools de negocio (el router determinista las maneja).
func buildConversationPrompt() string {
	return "Eres el asistente de WhatsApp de Capital Motors (concesionario DONGFENG). " +
		"El empleado que te escribe es un asesor comercial ya identificado. " +
		"Respondes en español, con mensajes cortos y amables. " +
		"El bot maneja automáticamente las cotizaciones, las fichas/fotos de " +
		"vehículos, el catálogo de precios, el listado de cotizaciones y la " +
		"impresión de documentos: no intentes hacerlos tú ni inventes precios ni " +
		"vehículos. Si el usuario pregunta algo que depende de esos datos, dile " +
		"que ya se está procesando o guíalo con el comando /cotizar. Si no sabes " +
		"algo, sé honesto y sugiere hablar con un supervisor."
}

// messageText extrae el texto plano de un mensaje.
func messageText(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	if t := m.GetConversation(); t != "" {
		return strings.TrimSpace(t)
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		return strings.TrimSpace(ext.GetText())
	}
	if btn := m.GetButtonsResponseMessage(); btn != nil {
		return strings.TrimSpace(btn.GetSelectedDisplayText())
	}
	if list := m.GetListResponseMessage(); list != nil {
		return strings.TrimSpace(list.GetTitle())
	}
	return ""
}

// sendText encola un mensaje de texto a un chat.
func (b *Bot) sendText(jid types.JID, text string) {
	if text == "" {
		return
	}
	b.out <- outMsg{jid: jid, text: text}
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " ,"); i > 0 {
		return s[:i]
	}
	return s
}
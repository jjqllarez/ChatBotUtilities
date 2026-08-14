package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/cotizaciones"
	"bot/internal/empleados"
	"bot/internal/pdf"
	"bot/internal/supabase"
)

var (
	errNoFlow          = errors.New("sin flujo activo")
	errNeedsAssistant  = errors.New("el mensaje no pertenece al flujo; delegar al asistente")
)

const (
	stepFormaPago = "forma_pago"
	stepVehiculo  = "vehiculo"
	stepPrecio    = "precio"
	stepPlan      = "plan"
	stepInicial   = "inicial"
	stepCliente   = "cliente"
	stepPickCliente = "pick_cliente"
	stepConfirmar = "confirmar"
	stepClienteCedula = "cliente_cedula"
)

// quoteDraft guarda el avance del flujo /cotizar de un chat.
type quoteDraft struct {
	Step       string                 `json:"step"`
	FormaPago  string                 `json:"forma_pago"`
	Versions   []cotizaciones.Version `json:"versions"`
	Version    *cotizaciones.Version  `json:"version"`
	TipoPrecio string                 `json:"tipo_precio"`
	Plans      []cotizaciones.Plan    `json:"plans"`
	Plan       *cotizaciones.Plan     `json:"plan"`
	Inicial    float64                `json:"inicial"`
	Resultado  *cotizaciones.ResultadoMotor `json:"resultado"`
	Cliente    *cotizaciones.Cliente  `json:"cliente"`
	Candidates []cotizaciones.Cliente `json:"candidates"`
	// ClienteNuevo guarda datos de cliente a medio capturar (p. ej. nombre +
	// teléfono) mientras el flujo pide la cédula que falta.
	ClienteNuevo *cotizaciones.CrearClienteParams `json:"cliente_nuevo,omitempty"`
}

// flowManager maneja la máquina de estados por teléfono y las órdenes
// directas desde el LLM.
type flowManager struct {
	supa *supabase.Client
	bot  *Bot

	mu       sync.Mutex
	sessions map[string]*quoteDraft
	lastList map[string][]cotizaciones.CotizacionBreve
}

func newFlowManager(supa *supabase.Client, b *Bot) *flowManager {
	return &flowManager{supa: supa, bot: b, sessions: make(map[string]*quoteDraft), lastList: make(map[string][]cotizaciones.CotizacionBreve)}
}

func (f *flowManager) active(ctx context.Context, phone string) bool {
	return f.ensureSession(ctx, phone) != nil
}

// ensureSession devuelve la sesión en memoria del teléfono, recargándola desde
// el estado persistido (Supabase) si el bot se reinició. Crea la sesión en
// memoria si pudo recuperarla.
func (f *flowManager) ensureSession(ctx context.Context, phone string) *quoteDraft {
	f.mu.Lock()
	s, ok := f.sessions[phone]
	f.mu.Unlock()
	if ok {
		return s
	}
	if s = f.loadDraft(ctx, phone); s != nil {
		f.mu.Lock()
		f.sessions[phone] = s
		f.mu.Unlock()
	}
	return s
}

// stepHint describe el paso actual del flujo /cotizar para que el asistente
// LLM recuerde al empleado cómo continuar cuando interrumpe con otro tema
// (así no responde con basura ni ignora el flujo en curso).
func (f *flowManager) stepHint(ctx context.Context, phone string) string {
	s := f.ensureSession(ctx, phone)
	if s == nil {
		return ""
	}
	switch s.Step {
	case stepFormaPago:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso FORMA DE PAGO. Si su mensaje no responde a eso, atiéndelo muy breve y al final recuérdale que escriba 1 (Contado) o 2 (Crédito)."
	case stepVehiculo:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso VEHÍCULO. Si su mensaje no es un número de vehículo, atiéndelo muy breve y al final recuérdale que escriba el número del vehículo que quiera (1-" + itoa(len(s.Versions)) + ")."
	case stepPrecio:
		v := s.Version
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso TIPO DE PRECIO para " + v.MarcaNombre + " " + displayName(*v) + ". Si su mensaje no responde a eso, atiéndelo muy breve y al final recuérdale que escriba 1 (Estandar), 2 (Premium) o 3 (Flota)."
	case stepPlan:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso PLAN DE FINANCIAMIENTO (Crédito). Si su mensaje no es un número de plan, atiéndelo muy breve y al final recuérdale que escriba el número del plan."
	case stepInicial:
		precio := s.Version.PrecioPorTipo(s.TipoPrecio)
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso INICIAL del plan " + s.Plan.NombrePlan + " (precio " + formatQ(precio) + " USD). Si su mensaje no responde a eso, atiéndelo muy breve y al final recuérdale que escriba el inicial en USD (ej: 25000) o porcentaje (ej: 50%)."
	case stepCliente:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso CLIENTE. Si su mensaje no es una cédula o nombre, atiéndelo muy breve y al final recuérdale que escriba la cédula (V-12345678) o el nombre del cliente."
	case stepPickCliente:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO y debe ELEGIR UN CLIENTE de la lista. Si su mensaje no es un número, atiéndelo muy breve y al final recuérdale que escriba el número del cliente correcto."
	case stepClienteCedula:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso CÉDULA del cliente nuevo. Si su mensaje no es una cédula, atiéndelo muy breve y al final recuérdale que escriba la cédula con su letra (ej: V-12345678)."
	case stepConfirmar:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso CONFIRMAR. Si su mensaje no es si/no, atiéndelo muy breve y al final recuérdale que responda 'si' para confirmar o 'no' para cancelar."
	}
	return ""
}

// saveDraft persiste el borrador de cotización en curso del teléfono.
func (f *flowManager) saveDraft(ctx context.Context, phone string) {
	f.mu.Lock()
	s, ok := f.sessions[phone]
	f.mu.Unlock()
	if !ok {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		f.bot.log.Printf("Guardando borrador de %s: %v", phone, err)
		return
	}
	if err := f.bot.state.Set(ctx, phone, map[string]any{"draft": string(data)}); err != nil {
		f.bot.log.Printf("Guardando borrador de %s: %v", phone, err)
	}
}

// loadDraft recupera el borrador persistido del teléfono, si existe.
func (f *flowManager) loadDraft(ctx context.Context, phone string) *quoteDraft {
	st, err := f.bot.state.Get(ctx, phone)
	if err != nil {
		return nil
	}
	raw, ok := st["draft"]
	if !ok {
		return nil
	}
	data, _ := raw.(string)
	if data == "" {
		return nil
	}
	var s quoteDraft
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		f.bot.log.Printf("Cargando borrador de %s: %v", phone, err)
		return nil
	}
	// Un borrador sin paso es un estado corrupto/vacío: no resumirlo.
	if s.Step == "" {
		f.bot.log.Printf("Borrador de %s sin paso; descartado", phone)
		return nil
	}
	return &s
}

// clearDraft elimina el borrador persistido del teléfono.
func (f *flowManager) clearDraft(ctx context.Context, phone string) {
	_ = f.bot.state.Set(ctx, phone, map[string]any{"draft": nil})
}

// handleCommand procesa comandos explícitos (/cotizar, /listar, /cancelar).
func (f *flowManager) handleCommand(phone string, emp *empleados.Empleado, text string) bool {
	cmd := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.HasPrefix(cmd, "/cotizar"):
		f.start(phone, emp)
		return true
	case strings.HasPrefix(cmd, "/listar"):
		go f.list(phone, emp)
		return true
	case strings.HasPrefix(cmd, "/cancelar"):
		f.cancel(phone)
		return true
	case strings.HasPrefix(cmd, "/ayuda"):
		f.bot.sendText(jidFor(phone), mensajeAyuda)
		return true
	}
	return false
}

const mensajeAyuda = "Comandos disponibles:\n" +
	"• /cotizar - iniciar una cotización\n" +
	"• /listar - ver tus últimas cotizaciones\n" +
	"• /cancelar - cancelar la cotización en curso\n\n" +
	"También puedes pedirme cosas en lenguaje natural, como: cotizar un vehículo para un cliente."

func (f *flowManager) start(phone string, emp *empleados.Empleado) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.bot.clearStateKey(ctx, phone, "ficha_pick")

	versions, err := cotizaciones.ObtenerVersiones(ctx, f.supa, emp.SocioComercial)
	if err != nil {
		f.bot.sendText(jidFor(phone), "No pude cargar el catálogo. Intenta de nuevo.")
		return
	}
	plans, err := cotizaciones.ObtenerPlanes(ctx, f.supa, emp.SocioComercial)
	if err != nil {
		plans = nil
	}

	f.mu.Lock()
	f.sessions[phone] = &quoteDraft{
		Step:     stepFormaPago,
		Versions: versions,
		Plans:    plans,
	}
	f.mu.Unlock()
	f.saveDraft(ctx, phone)

	f.bot.sendText(jidFor(phone), "Vamos a cotizar. ¿Forma de pago?\n1) Contado\n2) Crédito")
}

func (f *flowManager) cancel(phone string) {
	ctx, cancelCtx := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelCtx()
	// Recargar el borrador persistido: tras un reinicio la sesión en memoria
	// puede no existir y el /cancelar no debe quedarse mudo.
	s := f.ensureSession(ctx, phone)
	f.mu.Lock()
	delete(f.sessions, phone)
	f.mu.Unlock()
	if s != nil {
		f.clearDraft(ctx, phone)
	}
	f.bot.sendText(jidFor(phone), "Cotización cancelada.")
}

func (f *flowManager) process(ctx context.Context, phone string, emp *empleados.Empleado, text string) error {
	s := f.ensureSession(ctx, phone)
	if s == nil {
		return errNoFlow
	}
	defer f.saveDraft(ctx, phone)

	switch s.Step {
	case stepFormaPago:
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "1", "contado":
			s.FormaPago = "Contado"
			f.askVehiculo(phone, s)
		case "2", "credito", "crédito":
			s.FormaPago = "Credito"
			f.askVehiculo(phone, s)
		default:
			// Fallback: "la 1", "opción 2", etc.
			if idx, ok := parseIndex(text); ok && (idx == 1 || idx == 2) {
				s.FormaPago = "Contado"
				if idx == 2 {
					s.FormaPago = "Credito"
				}
				f.askVehiculo(phone, s)
				break
			}
			// No es una respuesta del paso: dejar que el asistente atienda
			// (ficha, catálogo, otra petición) sin perder el borrador.
			return errNeedsAssistant
		}
	case stepVehiculo:
		idx, ok := parseIndex(text)
		if !ok || idx < 1 || idx > len(s.Versions) {
			// Petición de catálogo/precios durante la elección de vehículo:
			// atender de forma determinista, sin depender del LLM.
			if esPeticionCatalogo(text) {
				f.bot.toolListar(context.Background(), jidFor(phone), emp, "")
				return nil
			}
			return errNeedsAssistant
		}
		v := s.Versions[idx-1]
		s.Version = &v
		s.Step = stepPrecio
		f.bot.sendText(jidFor(phone),
			"¿Tipo de precio para "+v.MarcaNombre+" "+displayName(v)+"?\n"+
				"1) Estandar ("+formatQ(v.PrecioEstandar)+")\n"+
				"2) Premium ("+formatQ(v.PrecioPremium)+")\n"+
				"3) Flota ("+formatQ(v.PrecioFlota)+")")
	case stepPrecio:
		var precioOK bool
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "1", "estandar", "estándar", "standard":
			s.TipoPrecio = "estandar"
			precioOK = true
		case "2", "premium":
			s.TipoPrecio = "premium"
			precioOK = true
		case "3", "flota":
			s.TipoPrecio = "flota"
			precioOK = true
		}
		// Fallback: textos tipo "el 3", "opción 2" → mapear el número.
		if !precioOK {
			if idx, ok := parseIndex(text); ok {
				switch idx {
				case 1:
					s.TipoPrecio = "estandar"
					precioOK = true
				case 2:
					s.TipoPrecio = "premium"
					precioOK = true
				case 3:
					s.TipoPrecio = "flota"
					precioOK = true
				}
			}
		}
		if !precioOK {
			return errNeedsAssistant
		}
		if s.FormaPago == "Contado" {
			s.Plan = nil
			f.askCliente(phone, s)
		} else {
			s.Step = stepPlan
			f.askPlan(phone, s)
		}
	case stepPlan:
		idx, ok := parseIndex(text)
		if !ok || idx < 1 || idx > len(s.Plans) {
			return errNeedsAssistant
		}
		p := s.Plans[idx-1]
		s.Plan = &p
		s.Step = stepInicial
		f.bot.sendText(jidFor(phone), fmt.Sprintf(
			"¿Cuánto será el inicial? (ej: 25000 o 50%%)\nEl mínimo del plan %s es %.2f%% del precio.",
			p.NombrePlan, p.InicialMinimaPorcentaje))
	case stepInicial:
		precio := s.Version.PrecioPorTipo(s.TipoPrecio)
		inicial, ok := parseInicial(text, precio)
		if !ok {
			// Si es numérico pero inválido, mejor re-preguntar aquí mismo;
			// si no es numérico, puede ser otra petición (catálogo, etc.).
			if _, aerr := parseAmount(text); aerr != nil {
				return errNeedsAssistant
			}
			f.bot.sendText(jidFor(phone),
				"Escribe el inicial como monto en USD (ej: 25000) o porcentaje (ej: 50%).")
			return nil
		}
		s.Inicial = inicial
		if s.Plan != nil && inicial < precio*s.Plan.InicialMinimaPorcentaje/100 {
			f.bot.sendText(jidFor(phone), fmt.Sprintf(
				"El inicial mínimo es %.2f%% del precio (%.2f USD). Escribe un monto mayor.",
				s.Plan.InicialMinimaPorcentaje, precio*s.Plan.InicialMinimaPorcentaje/100))
			return nil
		}
		res, err := cotizaciones.CalcularPlan(ctx, f.supa, s.Plan.ID, precio, inicial)
		if err != nil {
			f.bot.sendText(jidFor(phone), "No pude calcular el plan. Intenta de nuevo.")
			return nil
		}
		s.Resultado = res
		f.askCliente(phone, s)
	case stepCliente:
		if len(s.Candidates) == 0 {
			term := strings.TrimSpace(text)
			if term == "" {
				f.bot.sendText(jidFor(phone), "Escribe la cédula o el nombre del cliente.")
				return nil
			}
			// ¿El texto parece "TipoDoc,Cedula,Nombre,..."? -> registrar cliente.
			if p, ok := parseClienteLine(term); ok {
				id, err := cotizaciones.CrearCliente(ctx, f.supa, emp.SocioComercial, *p)
				if err != nil {
					f.bot.sendText(jidFor(phone), "No pude registrar el cliente: "+err.Error())
					return nil
				}
				s.Cliente = &cotizaciones.Cliente{
					ID:                id,
					SocioComercial:    emp.SocioComercial,
					TipoDocumento:     p.TipoDocumento,
					NumeroDocumento:   p.NumeroDocumento,
					NombreRazonSocial: p.NombreRazonSocial,
					TelefonoPrincipal: p.TelefonoPrincipal,
				}
				f.askConfirmar(phone, s)
				return nil
			}
			cands, err := cotizaciones.BuscarClientes(ctx, f.supa, emp.SocioComercial, term)
			if err != nil {
				f.bot.sendText(jidFor(phone), "Error buscando el cliente. Intenta de nuevo.")
				return nil
			}
			switch len(cands) {
			case 0:
				// El cliente no existe: si el texto trae nombre + teléfono/cédula,
				// lo registramos y seguimos el flujo (no quedarse esperando).
				if p, ok := parseNuevoCliente(term); ok {
					if p.NumeroDocumento == "" && p.TelefonoPrincipal != "" {
						// Falta la cédula: pedirla antes de registrar.
						s.ClienteNuevo = p
						s.Step = stepClienteCedula
						f.bot.sendText(jidFor(phone),
							"Escribe la cédula de "+p.NombreRazonSocial+" (ej: V-12345678).")
						return nil
					}
					id, err := cotizaciones.CrearCliente(ctx, f.supa, emp.SocioComercial, *p)
					if err != nil {
						f.bot.sendText(jidFor(phone), "No pude registrar el cliente: "+err.Error())
						return nil
					}
					s.Cliente = &cotizaciones.Cliente{
						ID:                id,
						SocioComercial:    emp.SocioComercial,
						TipoDocumento:     p.TipoDocumento,
						NumeroDocumento:   p.NumeroDocumento,
						NombreRazonSocial: p.NombreRazonSocial,
						TelefonoPrincipal: p.TelefonoPrincipal,
					}
					f.askConfirmar(phone, s)
					return nil
				}
				f.bot.sendText(jidFor(phone),
					"No encontré ese cliente. Escribe sus datos así:\n"+
						"TipoDoc,Cedula,Nombre,Telefono\n"+
						"Ejemplo: V,12345678,Juan Perez,04141234567")
				return nil
			case 1:
				s.Cliente = &cands[0]
				f.askConfirmar(phone, s)
				return nil
			default:
				s.Candidates = cands
				s.Step = stepPickCliente
				f.bot.sendText(jidFor(phone), "Encontré varios. Escribe el número del correcto:\n"+listClientes(cands))
				return nil
			}
		}
	case stepPickCliente:
		idx, ok := parseIndex(text)
		if !ok || idx < 1 || idx > len(s.Candidates) {
			return errNeedsAssistant
		}
		c := s.Candidates[idx-1]
		s.Cliente = &c
		s.Candidates = nil
		f.askConfirmar(phone, s)
	case stepClienteCedula:
		if s.ClienteNuevo == nil {
			return errNeedsAssistant
		}
		// El texto debe ser la cédula con su letra de tipo (V16573081, V-16573081
		// o V 16573081). Sin la letra no se puede saber el tipo de documento.
		tipoDoc, doc := parseDoc(text)
		if doc == "" {
			f.bot.sendText(jidFor(phone),
				"Escribe la cédula con su letra (ej: V-16573081).\n"+
					"La letra puede ser V, E, J, P o G.")
			return nil
		}
		if tipoDoc == "" {
			tipoDoc = "V"
		}
		p := s.ClienteNuevo
		p.TipoDocumento = tipoDoc
		p.NumeroDocumento = doc
		id, err := cotizaciones.CrearCliente(ctx, f.supa, emp.SocioComercial, *p)
		if err != nil {
			f.bot.sendText(jidFor(phone), "No pude registrar el cliente: "+err.Error())
			return nil
		}
		s.Cliente = &cotizaciones.Cliente{
			ID:                id,
			SocioComercial:    emp.SocioComercial,
			TipoDocumento:     p.TipoDocumento,
			NumeroDocumento:   p.NumeroDocumento,
			NombreRazonSocial: p.NombreRazonSocial,
			TelefonoPrincipal: p.TelefonoPrincipal,
		}
		s.ClienteNuevo = nil
		f.askConfirmar(phone, s)
	case stepConfirmar:
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "si", "sí", "s", "1", "confirmar":
			f.emit(ctx, phone, emp, s)
		case "no", "n", "0", "cancelar", "no confirmar":
			f.bot.sendText(jidFor(phone), "Cotización cancelada.")
			f.mu.Lock()
			delete(f.sessions, phone)
			f.mu.Unlock()
			f.clearDraft(context.Background(), phone)
		default:
			return errNeedsAssistant
		}
	}
	return nil
}

func (f *flowManager) askVehiculo(phone string, s *quoteDraft) {
	if len(s.Versions) == 0 {
		f.bot.sendText(jidFor(phone), "No hay vehículos disponibles.")
		return
	}
	s.Step = stepVehiculo
	var b strings.Builder
	b.WriteString("Elige el vehículo (escribe el número):\n")
	for i, v := range s.Versions {
		fmt.Fprintf(&b, "%d) %s %s\n", i+1, v.MarcaNombre, displayName(v))
	}
	f.bot.sendText(jidFor(phone), strings.TrimRight(b.String(), "\n"))
}

func (f *flowManager) askPlan(phone string, s *quoteDraft) {
	if len(s.Plans) == 0 {
		f.bot.sendText(jidFor(phone), "No hay planes de financiamiento. Escribe /cancelar.")
		return
	}
	s.Step = stepPlan
	var b strings.Builder
	b.WriteString("Elige el plan de financiamiento:\n")
	for i, p := range s.Plans {
		fmt.Fprintf(&b, "%d) %s\n", i+1, p.NombrePlan)
	}
	f.bot.sendText(jidFor(phone), strings.TrimRight(b.String(), "\n"))
}

func (f *flowManager) askCliente(phone string, s *quoteDraft) {
	s.Step = stepCliente
	s.Candidates = nil
	f.bot.sendText(jidFor(phone),
		"¿Cliente? Escribe su cédula (V-12345678) o nombre.")
}

func (f *flowManager) askConfirmar(phone string, s *quoteDraft) {
	s.Step = stepConfirmar
	var b strings.Builder
	b.WriteString("Confirma los datos:\n")
	fmt.Fprintf(&b, "Vehículo: %s %s\n", s.Version.MarcaNombre, displayName(*s.Version))
	fmt.Fprintf(&b, "Precio (%s): %s USD\n", s.TipoPrecio, formatQ(s.Version.PrecioPorTipo(s.TipoPrecio)))
	if s.Plan != nil {
		fmt.Fprintf(&b, "Plan: %s\nInicial: %s USD\n", s.Plan.NombrePlan, formatQ(s.Inicial))
	}
	fmt.Fprintf(&b, "Cliente: %s (C.I. %s)\n", s.Cliente.NombreRazonSocial, s.Cliente.NumeroDocumento)
	b.WriteString("¿Confirmar? (si/no)")
	f.bot.sendText(jidFor(phone), b.String())
}

// emit guarda la cotización y envía PDF + vista previa.
func (f *flowManager) emit(ctx context.Context, phone string, emp *empleados.Empleado, s *quoteDraft) {
	numero := f.nextNumero(ctx, emp.SocioComercial)
	in := cotizaciones.EmitirInput{
		UserID:            emp.UserID,
		ClienteID:         s.Cliente.ID,
		Version:           *s.Version,
		TipoPrecio:        s.TipoPrecio,
		FormaPago:         s.FormaPago,
		Inicial:           s.Inicial,
		NumeroPresupuesto: numero,
		Plan:              s.Plan,
		Resultado:         s.Resultado,
	}
	id, err := cotizaciones.EmitirCotizacion(ctx, f.supa, in)
	if err != nil {
		f.bot.sendText(jidFor(phone), "Error guardando la cotización: "+err.Error())
		return
	}
	det, err := cotizaciones.ObtenerDetalle(ctx, f.supa, id)
	if err != nil {
		f.bot.sendText(jidFor(phone), "Cotización guardada ("+numero+"), pero no pude generar el comprobante.")
		return
	}
	to := jidFor(phone)
	f.bot.sendText(to, "Cotización "+numero+" generada ✓")

	// PDF (con fallback a fpdf) + vista previa en imagen.
	var pdfBytes []byte
	pdfBytes, err = pdf.RenderPDF(det)
	if err != nil {
		pdfBytes, _ = pdf.BuildCotizacion(det)
	}
	if len(pdfBytes) > 0 {
		f.bot.sendMediaQueued(to, pdfBytes, "application/pdf", numero+".pdf", false)
	}
	if png, perr := pdf.RenderPNG(det); perr == nil && len(png) > 0 {
		f.bot.sendMediaQueued(to, png, "image/png", numero+".png", true)
	}

	f.mu.Lock()
	delete(f.sessions, phone)
	f.mu.Unlock()
	f.clearDraft(ctx, phone)
}

// nextNumero genera COT-YYMMDD-XXX.
func (f *flowManager) nextNumero(ctx context.Context, socioID int64) string {
	today := time.Now().Format("060102")
	count := 0
	if list, err := cotizaciones.ListarCotizaciones(ctx, f.supa, socioID, 200); err == nil {
		for _, c := range list {
			if strings.HasPrefix(c.NumeroPresupuesto, "COT-"+today) {
				count++
			}
		}
	}
	return fmt.Sprintf("COT-%s-%03d", today, count+1)
}

// list envía las cotizaciones del mes en curso. Los vendedores solo ven las
// suyas; los administradores ven las de todos los empleados de su socio.
func (f *flowManager) list(phone string, emp *empleados.Empleado) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	list, err := f.listadoCotizaciones(ctx, phone, emp)
	if err != nil {
		f.bot.sendText(jidFor(phone), "No pude listar las cotizaciones.")
		return
	}
	if len(list) == 0 {
		f.bot.sendText(jidFor(phone), "No hay cotizaciones en este mes todavía. Usa /cotizar para crear una.")
		return
	}

	var b strings.Builder
	b.WriteString("Cotizaciones del mes en curso:\n")
	for i, c := range list {
		fmt.Fprintf(&b, "%d. %s • %s • %s USD • %s\n",
			i+1, c.NumeroPresupuesto, shortDate(c.CreatedAt), formatQ(c.PrecioVehiculo), c.Cliente)
	}
	b.WriteString("\n¿Deseas imprimir alguna cotización de la lista? Dime el número.")
	texto := strings.TrimRight(b.String(), "\n")
	f.bot.sendText(jidFor(phone), texto)
}

// listadoCotizaciones consulta las cotizaciones del mes según el rol del
// empleado y guarda el resultado en lastList para imprimir después.
func (f *flowManager) listadoCotizaciones(ctx context.Context, phone string, emp *empleados.Empleado) ([]cotizaciones.CotizacionBreve, error) {
	cargo, err := empleados.CargoActual(ctx, f.supa, emp.ID)
	if err != nil {
		f.bot.log.Printf("Cargo de %s: %v", phone, err)
	}
	var empleadoID int64
	if !strings.EqualFold(cargo, "ADMINISTRADOR") {
		empleadoID = emp.ID
	}
	list, err := cotizaciones.ListarCotizacionesMes(ctx, f.supa, emp.SocioComercial, empleadoID, 50)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.lastList[phone] = list
	f.mu.Unlock()
	if data, err := json.Marshal(list); err == nil {
		st, _ := f.bot.state.Get(ctx, phone)
		st["last_list"] = string(data)
		_ = f.bot.state.Set(ctx, phone, st)
	}
	return list, nil
}

// pickCotizacion devuelve la cotización por su índice (1-based) del último
// listado enviado al teléfono y su ID.
func (f *flowManager) pickCotizacion(phone string, indice int) (cotizaciones.CotizacionBreve, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list, ok := f.lastList[phone]
	if !ok || indice < 1 || indice > len(list) {
		return cotizaciones.CotizacionBreve{}, false
	}
	return list[indice-1], true
}

// restoreLastList recupera el último listado persistido en Supabase
// (bot_chat_state["last_list"]) para poder imprimir tras un reinicio.
func (f *flowManager) restoreLastList(ctx context.Context, phone string) {
	st, err := f.bot.state.Get(ctx, phone)
	if err != nil {
		return
	}
	raw, _ := st["last_list"].(string)
	if raw == "" {
		return
	}
	var list []cotizaciones.CotizacionBreve
	if json.Unmarshal([]byte(raw), &list) != nil || len(list) == 0 {
		return
	}
	f.mu.Lock()
	f.lastList[phone] = list
	f.mu.Unlock()
}

// resolverCotizacion localiza la cotización del listado por índice (1-based).
// Primero usa la lista en memoria; si no (p. ej. tras reinicio), recupera el
// último listado persistido en Supabase (bot_chat_history) y reconsulta para
// obtener el ID.
func (f *flowManager) resolverCotizacion(ctx context.Context, phone string, emp *empleados.Empleado, indice int) (cotizaciones.CotizacionBreve, bool) {
	if c, ok := f.pickCotizacion(phone, indice); ok {
		return c, true
	}
	f.restoreLastList(ctx, phone)
	if c, ok := f.pickCotizacion(phone, indice); ok {
		return c, true
	}
	numero := f.numeroDeHistorial(ctx, phone, indice)
	if numero == "" {
		return cotizaciones.CotizacionBreve{}, false
	}
	list, err := f.listadoCotizaciones(ctx, phone, emp)
	if err != nil {
		return cotizaciones.CotizacionBreve{}, false
	}
	for _, c := range list {
		if c.NumeroPresupuesto == numero {
			return c, true
		}
	}
	return cotizaciones.CotizacionBreve{}, false
}

// numeroDeHistorial extrae el numero_presupuesto del índice N del último
// listado "Cotizaciones del mes en curso" guardado en el historial.
func (f *flowManager) numeroDeHistorial(ctx context.Context, phone string, indice int) string {
	hist, err := f.bot.history.Recent(ctx, phone, 60)
	if err != nil {
		return ""
	}
	prefix := fmt.Sprintf("%d. ", indice)
	for i := len(hist) - 1; i >= 0; i-- {
		h := hist[i]
		if !strings.Contains(h.Content, "Cotizaciones del mes en curso:") {
			continue
		}
		for _, line := range strings.Split(h.Content, "\n") {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			rest := strings.TrimPrefix(line, prefix)
			if j := strings.Index(rest, " • "); j > 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// shortDate convierte un timestamp ISO a "dd/mm/aaaa"; si no se puede parsear
// devuelve los 10 primeros caracteres (aaaa-mm-dd).
func shortDate(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		if len(iso) >= 10 {
			return iso[:10]
		}
		return iso
	}
	return t.Format("02/01/2006")
}

// esPeticionCatalogo detecta pedidos de la lista de vehículos o sus precios
// (catálogo, precios, "qué vehículos hay", etc.) para atenderlos sin LLM.
func esPeticionCatalogo(text string) bool {
	low := norm(text)
	switch low {
	case "lista", "listado", "listado de vehiculos", "listado de vehículos",
		"catalogo", "catálogo", "precios", "precios de los carros",
		"lista de precios", "lista de vehiculos", "lista de vehículos",
		"dame la lista", "dame el catalogo", "dame el catálogo",
		"que vehiculos hay", "qué vehículos hay", "que carros hay",
		"qué carros hay", "vehiculos disponibles", "vehículos disponibles",
		"dame la lista de vehiculos con precios", "dame la lista de vehículos con precios":
		return true
	}
	return strings.Contains(low, "lista de vehiculos") ||
		strings.Contains(low, "lista de vehículos") ||
		(strings.Contains(low, "lista") && (strings.Contains(low, "precio") || strings.Contains(low, "vehiculo") || strings.Contains(low, "vehículo") || strings.Contains(low, "carro")))
}

var reIndexNum = regexp.MustCompile(`\d+`)

// firstPositiveNumber extrae el primer número del texto rechazando negativos
// ("-1" no devuelve 1). Devuelve (n, true) con n >= 1.
func firstPositiveNumber(text string) (int, bool) {
	loc := reIndexNum.FindStringIndex(text)
	if loc == nil {
		return 0, false
	}
	if loc[0] > 0 && text[loc[0]-1] == '-' {
		return 0, false
	}
	n, err := strconv.Atoi(text[loc[0]:loc[1]])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parseIndex extrae el primer número del texto. Acepta "20", "La 20",
// "el vehículo 3", "opción 2", etc. Rechaza textos sin números y negativos.
func parseIndex(text string) (int, bool) {
	return firstPositiveNumber(strings.TrimSpace(text))
}

func parseAmount(text string) (float64, error) {
	clean := strings.ReplaceAll(strings.TrimSpace(text), ",", "")
	clean = strings.ReplaceAll(clean, ".", "")
	var ent, dec int
	_, err := fmt.Sscanf(clean, "%d", &ent)
	if err != nil {
		return 0, err
	}
	_ = dec
	return float64(ent), nil
}

// parseInicial interpreta la respuesta del paso "inicial": porcentaje
// ("50", "50%", "50 por ciento", "el 50") o moneda USD ("25000", "$25000",
// "25000 USD"). Regla: un número sin marcador <= 100 se interpreta como
// porcentaje del precio; > 100 como monto en USD.
func parseInicial(text string, precio float64) (float64, bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return 0, false
	}
	esPct := strings.Contains(t, "%") ||
		strings.Contains(t, "por ciento") || strings.Contains(t, "porciento") ||
		strings.Contains(t, "porcentaje")
	esMoneda := strings.Contains(t, "$") || strings.Contains(t, "usd") ||
		strings.Contains(t, "dolares") || strings.Contains(t, "dólares")
	n, ok := firstPositiveNumber(t)
	if !ok {
		return 0, false
	}
	switch {
	case esPct:
		return precio * float64(n) / 100, true
	case esMoneda:
		return float64(n), true
	default:
		if n <= 100 {
			return precio * float64(n) / 100, true
		}
		return float64(n), true
	}
}

func parseClienteLine(text string) (*cotizaciones.CrearClienteParams, bool) {
	parts := strings.Split(text, ",")
	if len(parts) < 3 {
		return nil, false
	}
	tipoDoc := strings.TrimSpace(parts[0])
	cedula := strings.TrimSpace(parts[1])
	// El nombre puede contener comas; unimos el resto salvo el último (teléfono).
	nombre := strings.TrimSpace(strings.Join(parts[2:], ","))
	telefono := ""
	if i := strings.LastIndex(nombre, ","); i >= 0 {
		telefono = strings.TrimSpace(nombre[i+1:])
		nombre = strings.TrimSpace(nombre[:i])
	}
	if tipoDoc == "" || cedula == "" || nombre == "" {
		return nil, false
	}
	return &cotizaciones.CrearClienteParams{
		TipoDocumento:    tipoDoc,
		NumeroDocumento:  cedula,
		NombreRazonSocial: nombre,
		TelefonoPrincipal: telefono,
	}, true
}

var (
	reClienteTelefono = regexp.MustCompile(`\+?\d{10,13}`)
	reClienteDoc      = regexp.MustCompile(`(?i)\b[VEJPGvejpg]\s*-?\s*\d{4,10}`)
	reNoLetras        = regexp.MustCompile(`[^\p{L}\s]+`)
)

// parseDoc extrae tipo de documento (V/E/J/P/G) y número de un texto que solo
// contiene la cédula. Acepta "V16573081", "V-16573081", "V 16573081" (con o sin
// separador) pero NO un número pelado: sin la letra no se puede saber el tipo.
func parseDoc(text string) (tipoDoc, doc string) {
	term := strings.TrimSpace(text)
	if term == "" {
		return "", ""
	}
	if m := reClienteDoc.FindString(term); m != "" {
		tipoDoc = strings.ToUpper(m[:1])
		doc = strings.TrimLeft(strings.TrimSpace(m[1:]), "- ")
		return tipoDoc, doc
	}
	// Número pelado sin letra: no aceptar, el flujo debe preguntar el tipo.
	return "", ""
}

// parseNuevoCliente extrae nombre + teléfono/cédula de un texto libre escrito
// por el asesor (p. ej. "Juan Perez 04141234567" o "V-12345678 Maria Lopes").
// Devuelve los datos para registrar al cliente y true si hay datos suficientes.
func parseNuevoCliente(text string) (*cotizaciones.CrearClienteParams, bool) {
	term := strings.TrimSpace(text)
	if term == "" {
		return nil, false
	}
	phone := ""
	if m := reClienteTelefono.FindString(term); m != "" {
		phone = m
	}
	tipoDoc, doc := "", ""
	if m := reClienteDoc.FindString(term); m != "" {
		tipoDoc = strings.ToUpper(m[:1])
		doc = strings.TrimLeft(strings.TrimSpace(m[1:]), "- ")
	}
	nombre := strings.TrimSpace(strings.Join(strings.Fields(reNoLetras.ReplaceAllString(term, " ")), " "))
	if nombre == "" {
		return nil, false
	}
	if doc == "" && phone == "" {
		return nil, false
	}
	// Si no hay cédula, no usar el teléfono como documento: el flujo pedirá
	// la cédula antes de registrar (ver stepCliente).
	if doc == "" {
		return &cotizaciones.CrearClienteParams{
			NombreRazonSocial: nombre,
			TelefonoPrincipal: phone,
		}, true
	}
	return &cotizaciones.CrearClienteParams{
		TipoDocumento:    tipoDoc,
		NumeroDocumento:  doc,
		NombreRazonSocial: nombre,
		TelefonoPrincipal: phone,
	}, true
}

func listClientes(cands []cotizaciones.Cliente) string {
	var b strings.Builder
	for i, c := range cands {
		fmt.Fprintf(&b, "%d) %s (C.I. %s%s)\n", i+1, c.NombreRazonSocial, c.TipoDocumento, c.NumeroDocumento)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatQ formatea un número con separadores de Venezuela (1.234,56).
func formatQ(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	intPart, dec := splitDecimal(s)
	var b strings.Builder
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(".")
		}
		b.WriteByte(intPart[i])
	}
	if dec != "" {
		b.WriteString("," + dec)
	}
	return b.String()
}

func splitDecimal(s string) (string, string) {
	if i := strings.Index(s, "."); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// jidFor construye un JID de chat desde un número (sin '+'.
func jidFor(phone string) types.JID {
	return types.NewJID(phone, types.DefaultUserServer)
}
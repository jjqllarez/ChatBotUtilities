package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	errNoFlow         = errors.New("sin flujo activo")
	errNeedsAssistant = errors.New("el mensaje no pertenece al flujo; delegar al asistente")
)

const (
	stepFormaPago     = "forma_pago"
	stepVehiculo      = "vehiculo"
	stepPrecio        = "precio"
	stepPlan          = "plan"
	stepVariables     = "variables"
	stepCliente       = "cliente"
	stepPickCliente   = "pick_cliente"
	stepConfirmar     = "confirmar"
	stepClienteCedula = "cliente_cedula"
	stepClienteNombre = "cliente_nombre"
	stepPrecioManual  = "precio_manual"
)

// draftTTL es el tiempo de inactividad tras el cual un borrador de /cotizar
// se descarta (evita que un flujo abandonado capture mensajes posteriores).
const draftTTL = 60 * time.Minute

// varEdit representa una variable editable mostrada al usuario.
type varEdit struct {
	Token   string  // ID del token (ej: "seguro")
	Nombre  string  // Nombre legible
	Valor   float64 // Valor actual (default o editado)
	Formato string  // "Moneda", "Porcentaje", "Entero", "Crudo"
}

// quoteDraft guarda el avance del flujo /cotizar de un chat.
type quoteDraft struct {
	Step         string                           `json:"step"`
	FormaPago    string                           `json:"forma_pago"`
	Versions     []cotizaciones.Version           `json:"versions"`
	Version      *cotizaciones.Version            `json:"version"`
	TipoPrecio   string                           `json:"tipo_precio"`
	CustomPrice  float64                          `json:"custom_price,omitempty"`
	Plans        []cotizaciones.Plan              `json:"plans"`
	Plan         *cotizaciones.Plan               `json:"plan"`
	PlanVars     map[string]float64               `json:"plan_vars,omitempty"`
	VarEdits     []varEdit                        `json:"var_edits,omitempty"`
	Inicial      float64                          `json:"inicial"`
	Resultado    *cotizaciones.ResultadoMotor     `json:"resultado"`
	Cliente      *cotizaciones.Cliente            `json:"cliente"`
	Candidates   []cotizaciones.Cliente           `json:"candidates"`
	ClienteNuevo *cotizaciones.CrearClienteParams `json:"cliente_nuevo,omitempty"`
	SavedAt      time.Time                        `json:"saved_at,omitempty"`
}

// effectivePrice devuelve el precio a usar: el manual del admin si se definió,
// o el precio de lista según el tipo seleccionado.
func (s *quoteDraft) effectivePrice() float64 {
	if s.CustomPrice > 0 {
		return s.CustomPrice
	}
	if s.Version != nil {
		return s.Version.PrecioPorTipo(s.TipoPrecio)
	}
	return 0
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
// LLM recuerde al empleado cómo continuar cuando interrumpe con otro tema.
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
	case stepVariables:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso VARIABLES DEL PLAN. Si su mensaje no es un número de variable ni \"siguiente\", atiéndelo muy breve y al final recuérdale que puede editar una variable escribiendo su número, o \"siguiente\" para calcular."
	case stepPrecioManual:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso PRECIO MANUAL (admin). Si su mensaje no es un precio en USD, atiéndelo muy breve y al final recuérdale que escriba el precio del vehículo en USD (ej: 25000)."
	case stepCliente:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso CLIENTE. Si su mensaje no es una cédula o nombre, atiéndelo muy breve y al final recuérdale que escriba la cédula (V-12345678) o el nombre del cliente."
	case stepPickCliente:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO y debe ELEGIR UN CLIENTE de la lista. Si su mensaje no es un número, atiéndelo muy breve y al final recuérdale que escriba el número del cliente correcto."
	case stepClienteCedula:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso CÉDULA del cliente nuevo. Si su mensaje no es una cédula, atiéndelo muy breve y al final recuérdale que escriba la cédula con su letra (ej: V-12345678)."
	case stepClienteNombre:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO y debe escribir el NOMBRE del cliente nuevo (la cédula ya está capturada). Si su mensaje no es un nombre, atiéndelo muy breve y recuérdale que escriba el nombre."
	case stepConfirmar:
		return "IMPORTANTE: el empleado tiene un flujo /cotizar ACTIVO en el paso CONFIRMAR. Si su mensaje no es si/no, atiéndelo muy breve y al final recuérdale que responda 'si' para confirmar o 'no' para cancelar."
	}
	return ""
}

// saveDraft persiste el borrador de cotización en curso del teléfono.
func (f *flowManager) saveDraft(ctx context.Context, phone string) {
	f.mu.Lock()
	s, ok := f.sessions[phone]
	if ok {
		s.SavedAt = time.Now()
	}
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
	if s.Step == "" {
		f.bot.log.Printf("Borrador de %s sin paso; descartado", phone)
		return nil
	}
	if s.SavedAt.IsZero() || time.Since(s.SavedAt) > draftTTL {
		f.bot.log.Printf("Borrador de %s expirado (TTL); descartado", phone)
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
	case strings.HasPrefix(cmd, "/cobranza"):
		if fl := f.bot.flowRegistry.FindByName("cobranza"); fl != nil {
			fl.Iniciar(phone, emp)
		} else {
			f.bot.sendText(jidFor(phone), "El módulo de cobranzas no está disponible.")
		}
		return true
	case strings.HasPrefix(cmd, "/cancelar"):
		// Cancelar flujo del registry activo (p. ej. cobranza); si no, el
		// flujo de cotización legacy.
		if af := f.bot.flowRegistry.ActiveFlow(context.Background(), phone); af != nil && af.Nombre() != "cotizacion" {
			af.Cancelar(phone)
			f.bot.sendText(jidFor(phone), "Consulta cancelada.")
			return true
		}
		f.cancel(phone)
		return true
	case strings.HasPrefix(cmd, "/ayuda"):
		f.bot.sendText(jidFor(phone), mensajeAyuda)
		return true
	}
	return false
}

const mensajeComoCotizar = "Para hacer una cotización:\n" +
	"1. Escribe /cotizar para iniciar.\n" +
	"2. Elige la forma de pago: 1 (Contado) o 2 (Crédito).\n" +
	"3. Elige el vehículo escribiendo su número.\n" +
	"4. Elige el tipo de precio: 1 (Estándar), 2 (Premium) o 3 (Flota).\n" +
	"5. Elige el plan y edita las variables si lo necesitas.\n" +
	"6. Indica la cédula (V-12345678) o el nombre del cliente.\n" +
	"7. Confirma y te envío la cotización en PDF e imagen.\n\n" +
	"Si ya sabes lo que quieres, escribe /cotizar y te guío paso a paso."

const mensajeAyuda = "Comandos disponibles:\n" +
	"• /cotizar - iniciar una cotización\n" +
	"• /listar - ver tus últimas cotizaciones\n" +
	"• /cobranza - consultar cobranzas (vencidas, por vencer, pagadas)\n" +
	"• /cancelar - cancelar la operación en curso\n\n" +
	"También puedes pedirme cosas en lenguaje natural, como: cotizar un vehículo para un cliente."

func (f *flowManager) start(phone string, emp *empleados.Empleado) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f.bot.clearStateKey(ctx, phone, "ficha_pick")
	f.bot.clearStateKey(ctx, phone, "last_list")
	f.mu.Lock()
	delete(f.lastList, phone)
	f.mu.Unlock()

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
	s := f.ensureSession(ctx, phone)
	f.mu.Lock()
	delete(f.sessions, phone)
	f.mu.Unlock()
	if s != nil {
		f.clearDraft(ctx, phone)
	}
	for _, nombre := range []string{"catalogo_vehiculos", "registrar_cliente"} {
		if fl := f.bot.flowRegistry.FindByName(nombre); fl != nil {
			fl.Cancelar(phone)
		}
	}
	f.bot.sendText(jidFor(phone), "Cotización cancelada.")
}

func (f *flowManager) process(ctx context.Context, phone string, emp *empleados.Empleado, text string) error {
	s := f.ensureSession(ctx, phone)
	if s == nil {
		return errNoFlow
	}
	defer f.saveDraft(ctx, phone)

	lowCmd := strings.ToLower(strings.TrimSpace(text))
	if lowCmd == "cancelar" || lowCmd == "cancelar la cotizacion" || lowCmd == "cancelar cotizacion" {
		f.bot.sendText(jidFor(phone), "Cotización cancelada.")
		f.mu.Lock()
		delete(f.sessions, phone)
		f.mu.Unlock()
		f.clearDraft(ctx, phone)
		return nil
	}

	if rePickNumber.MatchString(strings.TrimSpace(text)) {
		f.mu.Lock()
		list, hasList := f.lastList[phone]
		f.mu.Unlock()
		if hasList {
			if n, ok := parseIndex(text); ok && n >= 1 && n <= len(list) {
				return errNeedsAssistant
			}
		}
	}

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
			if idx, ok := parseIndex(text); ok && (idx == 1 || idx == 2) {
				s.FormaPago = "Contado"
				if idx == 2 {
					s.FormaPago = "Credito"
				}
				f.askVehiculo(phone, s)
				break
			}
			return errNeedsAssistant
		}
	case stepVehiculo:
		idx, ok := parseIndex(text)
		if !ok || idx < 1 || idx > len(s.Versions) {
			if esPeticionCatalogo(text) {
				f.bot.toolListar(ctx, jidFor(phone), emp, "")
				return nil
			}
			return errNeedsAssistant
		}
		v := s.Versions[idx-1]
		s.Version = &v
		s.Step = stepPrecio
		msg := "¿Tipo de precio para " + v.MarcaNombre + " " + displayName(v) + "?\n" +
			"1) Estandar (" + formatQ(v.PrecioEstandar) + ")\n" +
			"2) Premium (" + formatQ(v.PrecioPremium) + ")\n" +
			"3) Flota (" + formatQ(v.PrecioFlota) + ")"
		if f.bot.isAdmin(ctx, emp) {
			msg += "\n4) Precio manual"
		}
		f.bot.sendText(jidFor(phone), msg)
	case stepPrecio:
		var precioOK bool
		var precioManual bool
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
		case "4", "manual":
			precioManual = true
		}
		if !precioOK && !precioManual {
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
				case 4:
					precioManual = true
				}
			}
		}
		if precioManual {
			s.Step = stepPrecioManual
			f.bot.sendText(jidFor(phone),
				"Escribe el precio del vehículo en USD.\nEjemplo: *\"25000\"* o *\"25.500,00\"*")
			return nil
		}
		if !precioOK {
			return errNeedsAssistant
		}
		if s.FormaPago == "Contado" {
			planContado := cotizaciones.ObtenerPlan(s.Plans, cotizaciones.PlanContadoID)
			if planContado != nil {
				s.Plan = planContado
				f.initPlanVars(s)
			} else {
				s.Plan = nil
			}
			f.showVarList(phone, s)
		} else {
			s.Step = stepPlan
			f.askPlan(phone, s)
		}
	case stepPrecioManual:
		clean := strings.ReplaceAll(strings.TrimSpace(text), ",", "")
		clean = strings.ReplaceAll(clean, "$", "")
		clean = strings.ReplaceAll(clean, " ", "")
		clean = strings.ReplaceAll(clean, "USD", "")
		clean = strings.ReplaceAll(clean, "usd", "")
		var precio float64
		if _, err := fmt.Sscanf(clean, "%f", &precio); err != nil || precio <= 0 {
			f.bot.sendText(jidFor(phone),
				"Precio no válido. Escribe un número mayor a 0.\nEjemplo: *\"25000\"* o *\"25.500,00\"*")
			return nil
		}
		s.CustomPrice = precio
		s.TipoPrecio = "manual"
		if s.FormaPago == "Contado" {
			planContado := cotizaciones.ObtenerPlan(s.Plans, cotizaciones.PlanContadoID)
			if planContado != nil {
				s.Plan = planContado
				f.initPlanVars(s)
			} else {
				s.Plan = nil
			}
			f.showVarList(phone, s)
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
		f.initPlanVars(s)
		f.showVarList(phone, s)
	case stepVariables:
		low := strings.ToLower(strings.TrimSpace(text))
		if low == "siguiente" || low == "next" || low == "ok" {
			s.Inicial = s.PlanVars["inicial"]
			if s.Plan != nil && s.Inicial < s.effectivePrice()*s.Plan.InicialMinimaPorcentaje/100 {
				minUSD := s.effectivePrice() * s.Plan.InicialMinimaPorcentaje / 100
				f.bot.sendText(jidFor(phone), fmt.Sprintf(
					"El inicial mínimo es %.0f%% del precio (%s USD).\nEscribe el nuevo inicial:",
					s.Plan.InicialMinimaPorcentaje, formatQ(minUSD)))
				s.Step = "var_edit_inicial"
				return nil
			}
			res, err := cotizaciones.CalcularPlan(ctx, f.supa, s.Plan.ID, s.PlanVars)
			if err != nil {
				f.bot.sendText(jidFor(phone), "No pude calcular el plan. Intenta de nuevo.")
				return nil
			}
			s.Resultado = res
			f.askCliente(phone, s)
			return nil
		}
		// Editar una variable por número
		idx, ok := parseIndex(text)
		if !ok || idx < 1 || idx > len(s.VarEdits) {
			return errNeedsAssistant
		}
		v := &s.VarEdits[idx-1]
		s.Step = "var_edit_" + v.Token
		f.bot.sendText(jidFor(phone), "Nuevo valor para "+v.Nombre+":")
		return nil
	case stepCliente:
		if len(s.Candidates) == 0 {
			term := strings.TrimSpace(text)
			if term == "" {
				f.bot.sendText(jidFor(phone), "Escribe la cédula o el nombre del cliente.")
				return nil
			}
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
				if p, ok := parseNuevoCliente(term); ok {
					if p.NumeroDocumento == "" && p.TelefonoPrincipal != "" {
						s.ClienteNuevo = p
						s.Step = stepClienteCedula
						f.bot.sendText(jidFor(phone),
							"Escribe la cédula de "+p.NombreRazonSocial+" (ej: V-12345678).")
						return nil
					}
					if p.NombreRazonSocial == "" {
						s.ClienteNuevo = p
						s.Step = stepClienteNombre
						f.bot.sendText(jidFor(phone),
							"Dime el nombre del cliente (cédula "+p.TipoDocumento+"-"+p.NumeroDocumento+"):")
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
				if classifyIntent(term) != intentConversacion {
					return errNeedsAssistant
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
	case stepClienteNombre:
		if s.ClienteNuevo == nil {
			return errNeedsAssistant
		}
		p := s.ClienteNuevo
		name := strings.TrimSpace(text)
		if m := reClienteDoc.FindString(name); m != "" {
			name = strings.TrimSpace(strings.Replace(name, m, "", 1))
		}
		if name == "" {
			f.bot.sendText(jidFor(phone), "Escribe el nombre del cliente.")
			return nil
		}
		p.NombreRazonSocial = name
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
			f.clearDraft(ctx, phone)
		default:
			return errNeedsAssistant
		}
	default:
		// Manejar paso de edición de variable (var_edit_*)
		if strings.HasPrefix(s.Step, "var_edit_") {
			tokenID := strings.TrimPrefix(s.Step, "var_edit_")
			valor, ok := parseVarValue(text)
			if !ok {
				f.bot.sendText(jidFor(phone),
					"Escribe un número válido (ej: 2000, 16, 0.0226).")
				return nil
			}
			// Actualizar en PlanVars y VarEdits
			if s.PlanVars == nil {
				s.PlanVars = make(map[string]float64)
			}
			s.PlanVars[tokenID] = valor
			// Sincronizar CustomPrice / effectivePrice() cuando cambia precio_base
			if tokenID == "precio_base" {
				s.CustomPrice = valor
			}
			for i := range s.VarEdits {
				if s.VarEdits[i].Token == tokenID {
					s.VarEdits[i].Valor = valor
					break
				}
			}
			// Si se editó porcentaje_inicial, recalcular inicial
			if tokenID == "porcentaje_inicial" && s.PlanVars["precio_base"] > 0 {
				nuevoInicial := math.Round(s.PlanVars["precio_base"]*valor/100*100) / 100
				s.PlanVars["inicial"] = nuevoInicial
				for i := range s.VarEdits {
					if s.VarEdits[i].Token == "inicial" {
						s.VarEdits[i].Valor = nuevoInicial
						break
					}
				}
			}
			// Si se editó inicial directamente, también actualizar porcentaje_inicial
			if tokenID == "inicial" && s.PlanVars["precio_base"] > 0 {
				nuevoPct := valor / s.PlanVars["precio_base"] * 100
				s.PlanVars["porcentaje_inicial"] = math.Round(nuevoPct*100) / 100
				for i := range s.VarEdits {
					if s.VarEdits[i].Token == "porcentaje_inicial" {
						s.VarEdits[i].Valor = s.PlanVars["porcentaje_inicial"]
						break
					}
				}
			}
			s.Step = stepVariables
			f.showVarList(phone, s)
			return nil
		}
	}
	return nil
}

// initPlanVars inicializa PlanVars con los valores por defecto de los tokens
// editables del plan, auto-llenando precio_base del vehículo seleccionado.
func (f *flowManager) initPlanVars(s *quoteDraft) {
	s.PlanVars = make(map[string]float64)
	s.VarEdits = nil
	tokens := cotizaciones.EditableTokens(s.Plan)
	precio := s.effectivePrice()
	for _, t := range tokens {
		valor := t.Valor
		// Auto-llenar precio_base con el precio del vehículo
		if t.Token == "precio_base" {
			valor = precio
		}
		// Auto-calcular inicial de porcentaje_inicial si no existe como token editable
		if t.Token == "inicial" && valor == 0 {
			pct := 50.0
			if p, ok := s.PlanVars["porcentaje_inicial"]; ok && p > 0 {
				pct = p
			}
			valor = math.Round(precio*pct/100*100) / 100
		}
		s.PlanVars[t.Token] = valor
		s.VarEdits = append(s.VarEdits, varEdit{
			Token:   t.Token,
			Nombre:  t.Nombre,
			Valor:   valor,
			Formato: t.Formato,
		})
	}
}

// showVarList envía la lista de variables editables al usuario.
func (f *flowManager) showVarList(phone string, s *quoteDraft) {
	s.Step = stepVariables
	var b strings.Builder
	b.WriteString("Variables del plan " + s.Plan.NombrePlan + ":\n")
	for i, v := range s.VarEdits {
		fmt.Fprintf(&b, "%d) %s: %s\n", i+1, v.Nombre, formatVarValue(v.Valor, v.Formato))
	}
	b.WriteString("\nEscribe el número a editar, o *\"siguiente\"* para calcular.")
	f.bot.sendText(jidFor(phone), strings.TrimRight(b.String(), "\n"))
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
	fmt.Fprintf(&b, "Precio (%s): %s USD\n", s.TipoPrecio, formatQ(s.effectivePrice()))
	if s.Plan != nil {
		fmt.Fprintf(&b, "Plan: %s\n", s.Plan.NombrePlan)
		fmt.Fprintf(&b, "Inicial: %s USD\n", formatQ(s.Inicial))
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
		CustomPrecio:      s.CustomPrice,
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

	var pdfBytes []byte
	pdfBytes, err = pdf.RenderPDF(det)
	if err != nil {
		var ferr error
		pdfBytes, ferr = pdf.BuildCotizacion(det)
		if ferr != nil {
			f.bot.log.Printf("PDF fallback fpdf: %v", ferr)
		}
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

// nextNumero genera COT-YYMMDD-XXX a partir del maximo del dia.
func (f *flowManager) nextNumero(ctx context.Context, socioID int64) string {
	today := time.Now().Format("060102")
	prefix := "COT-" + today + "-"
	maxN := 0
	if list, err := cotizaciones.ListarCotizaciones(ctx, f.supa, socioID, 1000); err == nil {
		for _, c := range list {
			if !strings.HasPrefix(c.NumeroPresupuesto, prefix) {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimPrefix(c.NumeroPresupuesto, prefix)); err == nil && n > maxN {
				maxN = n
			}
		}
	}
	return fmt.Sprintf("%s%03d", prefix, maxN+1)
}

// list envía las cotizaciones del mes en curso.
func (f *flowManager) list(phone string, emp *empleados.Empleado) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	f.bot.clearStateKey(ctx, phone, "ficha_pick")
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

func (f *flowManager) pickCotizacion(phone string, indice int) (cotizaciones.CotizacionBreve, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list, ok := f.lastList[phone]
	if !ok || indice < 1 || indice > len(list) {
		return cotizaciones.CotizacionBreve{}, false
	}
	return list[indice-1], true
}

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

func parseIndex(text string) (int, bool) {
	return firstPositiveNumber(strings.TrimSpace(text))
}

func parseAmount(text string) (float64, error) {
	clean := strings.ReplaceAll(strings.TrimSpace(text), ",", "")
	clean = strings.ReplaceAll(clean, ".", "")
	var ent int
	_, err := fmt.Sscanf(clean, "%d", &ent)
	if err != nil {
		return 0, err
	}
	return float64(ent), nil
}

// parseVarValue parsea el valor ingresado por el usuario para una variable.
// Acepta números con o sin separadores, $, %. Devuelve (valor, ok).
func parseVarValue(text string) (float64, bool) {
	clean := strings.TrimSpace(text)
	if clean == "" {
		return 0, false
	}
	clean = strings.ReplaceAll(clean, ",", ".")
	clean = strings.ReplaceAll(clean, "$", "")
	clean = strings.ReplaceAll(clean, "%", "")
	clean = strings.ReplaceAll(clean, " ", "")
	clean = strings.ReplaceAll(clean, "USD", "")
	clean = strings.ReplaceAll(clean, "usd", "")
	clean = strings.ReplaceAll(clean, "meses", "")
	clean = strings.TrimSpace(clean)
	var v float64
	_, err := fmt.Sscanf(clean, "%f", &v)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// formatVarValue formatea el valor de una variable para mostrar al usuario.
func formatVarValue(valor float64, formato string) string {
	switch formato {
	case "Moneda":
		return formatQ(valor) + " USD"
	case "Porcentaje":
		return fmt.Sprintf("%.2f%%", valor)
	case "Entero":
		return strconv.Itoa(int(math.Round(valor)))
	default:
		// Crudo: mostrar con hasta 4 decimales, sin trailing zeros
		s := fmt.Sprintf("%.4f", valor)
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
		return s
	}
}

func parseClienteLine(text string) (*cotizaciones.CrearClienteParams, bool) {
	parts := strings.Split(text, ",")
	if len(parts) < 3 {
		return nil, false
	}
	tipoDoc := strings.TrimSpace(parts[0])
	cedula := strings.TrimSpace(parts[1])
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
		TipoDocumento:     tipoDoc,
		NumeroDocumento:   cedula,
		NombreRazonSocial: nombre,
		TelefonoPrincipal: telefono,
	}, true
}

var (
	reClienteTelefono = regexp.MustCompile(`\+?\d{10,13}`)
	reClienteDoc      = regexp.MustCompile(`(?i)\b[VEJPGvejpg]\s*-?\s*\d{4,10}`)
)

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
	return "", ""
}

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
	cleanTerm := term
	if m := reClienteDoc.FindString(term); m != "" {
		cleanTerm = strings.Replace(term, m, " ", 1)
	}
	if phone != "" {
		cleanTerm = strings.Replace(cleanTerm, phone, " ", 1)
	}
	nombre := strings.TrimSpace(strings.Join(strings.Fields(cleanTerm), " "))
	if doc == "" && phone == "" {
		return nil, false
	}
	if doc == "" {
		return &cotizaciones.CrearClienteParams{
			NombreRazonSocial: nombre,
			TelefonoPrincipal: phone,
		}, true
	}
	return &cotizaciones.CrearClienteParams{
		TipoDocumento:     tipoDoc,
		NumeroDocumento:   doc,
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

func jidFor(phone string) types.JID {
	return types.NewJID(phone, types.DefaultUserServer)
}

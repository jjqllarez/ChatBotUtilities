package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/cotizaciones"
	"bot/internal/empleados"
	"bot/internal/llm"
	"bot/internal/pdf"
	"bot/internal/supabase"
)

// runAssistant procesa un mensaje de un empleado con el asistente LLM y tools.
func (b *Bot) runAssistant(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string, flowHint string) {
	hist, err := b.history.Recent(ctx, phone, 40)
	if err != nil {
		hist = nil
	}
	if len(hist) == 0 {
		b.sendText(chat, greetingFor(emp))
	}

	state, _ := b.state.Get(ctx, phone)

	messages := []llm.Message{
		{Role: "system", Content: b.buildSystemPrompt(ctx, emp, state)},
	}
	if flowHint != "" {
		messages = append(messages, llm.Message{Role: "system", Content: flowHint})
	}
	for _, h := range hist {
		messages = append(messages, llm.Message{Role: h.Role, Content: h.Content})
	}
	messages = append(messages, llm.Message{Role: "user", Content: text})

	tools := b.toolDefinitions()

	// El LLM usa su propio contexto amplio: si el modelo principal cuelga, el
	// failover necesita tiempo para probar los siguientes.
	llmCtx, llmCancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer llmCancel()

	for iter := 0; iter < 5; iter++ {
		resp, err := b.llmClient.Chat(llmCtx, messages, tools)
		if err != nil {
			b.log.Printf("LLM %s: %v", phone, err)
			b.sendText(chat, "Estoy teniendo problemas de conexión con el asistente. Intenta de nuevo en un momento.")
			return
		}
		if len(resp.ToolCalls) == 0 {
			if resp.Content != "" {
				b.sendText(chat, resp.Content)
			}
			return
		}

		messages = append(messages, resp)
		sentDirect := false
		for _, tc := range resp.ToolCalls {
			result, direct := b.execTool(ctx, chat, phone, emp, tc)
			sentDirect = sentDirect || direct
			// En caso de fallo, igualmente se informa al modelo para que reaccione.
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
		if sentDirect {
			// La herramienta ya envió la respuesta al usuario; no dejar que el
			// modelo repita un resumen/confirmación.
			return
		}
	}
	b.sendText(chat, "No pude completar la solicitud. Puedes usar /cotizar para guiarme paso a paso.")
}

// greetingFor saluda según la hora de Venezuela.
func greetingFor(emp *empleados.Empleado) string {
	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.Local
	}
	h := time.Now().In(loc).Hour()
	parte := "Buenos días"
	switch {
	case h >= 12 && h < 19:
		parte = "Buenas tardes"
	case h >= 19 || h < 5:
		parte = "Buenas noches"
	}
	nombre := emp.NombreCompleto
	if nombre == "" {
		nombre = "colega"
	}
	return fmt.Sprintf("%s %s, ¿en qué puedo ayudarte?", parte, nombre)
}

// buildSystemPrompt arma el prompt del asistente incluyendo los planes de
// financiamiento reales del socio (determinista, desde la BD).
func (b *Bot) buildSystemPrompt(ctx context.Context, emp *empleados.Empleado, state map[string]any) string {
	var sb strings.Builder
	sb.WriteString("Eres el asistente de WhatsApp de Capital Motors (concesionario DONGFENG). " +
		"Respondes en español, con mensajes cortos y amables. " +
		"El empleado que te escribe es un asesor comercial ya identificado.\n" +
		"Puedes:\n" +
		"- Listar el catálogo de vehículos (listar_catalogo). Precios: premium/flota/estandar.\n" +
		"- Emitir cotizaciones (crear_cotizacion) directamente cuando el usuario dé cliente + vehículo + forma de pago + tipo de precio (+ plan) de una sola vez. Si el modelo tiene varias versiones, pregunta UNA vez cuál; si solo hay una, úsala. Si falta la inicial en Crédito, usa el mínimo del plan. No pidas confirmación.\n" +
		"- Enviar la ficha/card del vehículo como imagen SOLO cuando el usuario la pida explícitamente (dijo 'foto', 'card', 'ficha').\n" +
		"- Listar cotizaciones recientes (listar_cotizaciones).\n" +		"- Guardar datos que el usuario te dicte para la cotización en curso (recordar_dato).\n" +
		"REGLA listar_catalogo: SIEMPRE que el usuario pida la lista de vehículos, el catálogo, los precios, o algo equivalente (p. ej. 'dame la lista de vehículos', 'qué vehículos hay', 'catálogo', 'precios de los carros'), DEBES llamar la herramienta listar_catalogo. La herramienta envía el listado completo al usuario por WhatsApp con el formato correcto (lista numerada, nombres en *asteriscos*, precios con puntos y comas, línea en blanco entre vehículos). NO intentes armar ni redactar tú mismo la lista de vehículos: eso es responsabilidad de la herramienta. Cuando listar_catalogo devuelva confirmación, termina tu turno sin generar ningún texto adicional ni resumen de la lista.\n" +
		"REGLA listar_cotizaciones: SIEMPRE que el usuario pida sus cotizaciones, las cotizaciones del mes, o pregunte cuántas cotizaciones tiene, DEBES llamar la herramienta listar_cotizaciones. La herramienta envía el listado del mes en curso al usuario por WhatsApp. NO intentes listar ni contar cotizaciones tú mismo: eso es responsabilidad de la herramienta. Cuando listar_cotizaciones devuelva confirmación, termina tu turno sin generar ningún texto adicional. IMPORTANTE: si el usuario pide HACER, CREAR, GENERAR o CALCULAR una cotización (p. ej. 'hazme una cotización', 'cotiza el Z9'), NO uses listar_cotizaciones ni enviar el listado: eso es crear, no listar.\n" +
		"REGLA imprimir_cotizacion: SIEMPRE que el usuario diga 'imprime la cotización N', 'imprimir cotización N', 'muéstrame la cotización N' o similar (donde N es el número que apareció en el listado), DEBES llamar la herramienta imprimir_cotizacion con ese número en 'indice'. La herramienta envía el PDF y la imagen del vehículo por WhatsApp automáticamente. NO generes ni simules el PDF tú mismo. Cuando imprimir_cotizacion devuelva confirmación, termina tu turno sin generar texto adicional.\n" +
		"REGLA planes: Los planes de financiamiento NO se inventan. Solo usa los plan_id listados abajo. Si el usuario pide un plan inexistente, dile cuáles existen.\n" +
		"REGLA vehículo: NUNCA emitas una cotización con un vehículo que no esté explícitamente confirmado en la conversación (el usuario debió elegirlo o confirmarlo con su nombre/marca/modelo). Si continúas una conversación previa, usa el vehículo ya acordado. Si no hay un vehículo claro en el historial, PREGUNTA cuál antes de crear. Jamás adivines un vehículo.\n" +
		"Usa SIEMPRE listas numeradas en texto para catálogos y resultados. ")
	if emp.SocioComercial != 0 {
		fmt.Fprintf(&sb, "El socio comercial del empleado es el id %d.\n", emp.SocioComercial)
	}
	sb.WriteString("\nForma de pago válida: Contado o Credito.\n")
	sb.WriteString("\nPlanes de financiamiento disponibles (usa SIEMPRE el plan_id real, nunca inventes planes):\n")
	if plans, err := cotizaciones.ObtenerPlanes(ctx, b.supa, emp.SocioComercial); err == nil && len(plans) > 0 {
		for _, p := range plans {
			fmt.Fprintf(&sb, "- id %d: %s (inicial mínima %.2f%%)\n", p.ID, p.NombrePlan, p.InicialMinimaPorcentaje)
		}
	} else {
		sb.WriteString("- (sin planes activos)\n")
	}
	if len(state) > 0 {
		sb.WriteString("\nDatos pendientes guardados de esta conversación:\n")
		for k, v := range state {
			if k == "draft" {
				continue
			}
			fmt.Fprintf(&sb, "- %s: %v\n", k, v)
		}
	}
	return sb.String()
}

// toolDefinitions son las herramientas expuestas al modelo.
func (b *Bot) toolDefinitions() []llm.Tool {
	return []llm.Tool{
		{
			Type: "function",
			Function: llm.Function{
				Name:        "listar_catalogo",
				Description: "Lista los vehículos disponibles con sus precios y ENVÍA el listado completo al usuario por WhatsApp automáticamente (con formato correcto). Llámala siempre que el usuario pida la lista de vehículos, el catálogo o los precios. Opcionalmente filtra por término (marca/modelo/versión). No redactes tú el listado: usa esta herramienta.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"termino": map[string]any{"type": "string", "description": "Término de búsqueda opcional"},
					},
				},
			},
		},
		{
			Type: "function",
			Function: llm.Function{
				Name:        "crear_cotizacion",
				Description: "Emitir (guardar) una cotización con todos los datos y enviar PDF + imagen. No pedir confirmación.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"version_id":        map[string]any{"type": "integer", "description": "ID de la versión del vehículo"},
						"tipo_precio":       map[string]any{"type": "string", "description": "estandar, premium o flota"},
						"forma_pago":        map[string]any{"type": "string", "description": "Contado o Credito"},
						"plan_id":           map[string]any{"type": "integer", "description": "ID del plan (solo Crédito)"},
						"inicial":           map[string]any{"type": "number", "description": "Inicial en USD (solo Crédito)"},
						"precio_personalizado": map[string]any{"type": "number", "description": "Precio a medida en USD (solo administradores)"},
						"cliente_tipo_doc":  map[string]any{"type": "string", "description": "Tipo de documento del cliente (V/E/J)"},
						"cliente_cedula":    map[string]any{"type": "string", "description": "Número de documento/cedula"},
						"cliente_nombre":    map[string]any{"type": "string", "description": "Nombre o razón social"},
						"cliente_telefono":  map[string]any{"type": "string", "description": "Teléfono del cliente"},
						"cliente_actual":    map[string]any{"type": "string", "description": "Cédula o nombre de un cliente ya existente to buscar"},
					},
					"required": []string{"version_id", "forma_pago", "tipo_precio"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.Function{
				Name:        "enviar_ficha_vehiculo",
				Description: "Envía la ficha/card del vehículo como imagen PNG. Usar solo a petición explícita.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"version_id": map[string]any{"type": "integer", "description": "ID de la versión del vehículo"},
						"tipo_precio": map[string]any{"type": "string", "description": "estandar/premium/flota"},
						"precio_personalizado": map[string]any{"type": "number", "description": "Precio a medida (solo administradores)"},
					},
					"required": []string{"version_id"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.Function{
				Name:        "listar_cotizaciones",
				Description: "Lista las cotizaciones del mes en curso del empleado y las ENVÍA por WhatsApp automáticamente. Llámala siempre que el usuario pida sus cotizaciones o pregunte por ellas. No redactes tú el listado.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		{
			Type: "function",
			Function: llm.Function{
				Name:        "imprimir_cotizacion",
				Description: "Imprime (envía PDF + imagen con la foto del vehículo) una cotización del último listado. Llámala cuando el usuario diga 'imprime la cotización N', 'imprimir cotización N' o similar, donde N es el número mostrado en el listado.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"indice": map[string]any{"type": "integer", "description": "Número de la cotización en el listado (1, 2, 3...)"},
					},
					"required": []string{"indice"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.Function{
				Name:        "recordar_dato",
				Description: "Guarda un dato pendiente de la cotización en curso (p. ej. 'cliente', 'vehiculo').",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"clave":   map[string]any{"type": "string", "description": "Nombre del dato"},
						"valor":   map[string]any{"type": "string", "description": "Valor del dato"},
					},
					"required": []string{"clave", "valor"},
				},
			},
		},
	}
}

// execTool ejecuta una tool invocada por el modelo y devuelve el resultado en
// texto y si la herramienta ya envió la respuesta directamente al usuario
// (true). Las tools que envían directo (listado, cotizaciones, ficha) deben
// devolver true para que el asistente no genere un texto de más.
func (b *Bot) execTool(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, tc llm.ToolCall) (string, bool) {
	switch tc.Function.Name {
	case "listar_catalogo":
		return b.toolListar(ctx, chat, emp, tc.Function.Arguments), true
	case "crear_cotizacion":
		return b.toolCrearCotizacion(ctx, chat, phone, emp, tc.Function.Arguments), false
	case "enviar_ficha_vehiculo":
		return b.toolEnviarFicha(ctx, chat, phone, emp, tc.Function.Arguments), true
	case "listar_cotizaciones":
		b.flows.list(phone, emp)
		return "Se listaron las cotizaciones del mes por WhatsApp.", true
	case "imprimir_cotizacion":
		return b.toolImprimirCotizacion(ctx, chat, phone, emp, tc.Function.Arguments), true
	case "recordar_dato":
		return b.toolRecordar(ctx, phone, tc.Function.Arguments), false
	default:
		return "Herramienta desconocida: " + tc.Function.Name, false
	}
}

// --- Implementación de tools ---

func (b *Bot) toolListar(ctx context.Context, chat types.JID, emp *empleados.Empleado, argsJSON string) string {
	var args struct {
		Termino string `json:"termino"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	versions, err := cotizaciones.ObtenerVersiones(ctx, b.supa, emp.SocioComercial)
	if err != nil {
		return "Error listando catálogo: " + err.Error()
	}
	var b2 strings.Builder
	n := 0
	for _, v := range versions {
		if args.Termino != "" && !containsFold(v.MarcaNombre+v.ModeloNombre+v.NombreVersion, args.Termino) {
			continue
		}
		n++
		nombre := strings.TrimSpace(v.ModeloNombre + " " + v.NombreVersion)
		if v.ModeloNombre == "" {
			nombre = v.NombreVersion
		}
		if n > 1 {
			b2.WriteString("\n")
		}
		fmt.Fprintf(&b2, "%d. *%s* (ID: %d)\n   - Precios: Estandar: %s / Premium: %s / Flota: %s USD",
			n, nombre, v.ID,
			formatQ(v.PrecioEstandar), formatQ(v.PrecioPremium), formatQ(v.PrecioFlota))
	}
	if args.Termino != "" {
		fmt.Fprintf(&b2, "\n(búsqueda: %s)", args.Termino)
	}
	out := strings.TrimRight(b2.String(), "\n")
	if out == "" {
		return "No hay vehículos disponibles."
	}
	b.sendText(chat, out)
	return "Listado de " + itoa(n) + " vehículos enviado por WhatsApp con el formato exacto."
}

func (b *Bot) toolCrearCotizacion(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, argsJSON string) string {
	var args struct {
		VersionID           int64   `json:"version_id"`
		TipoPrecio          string  `json:"tipo_precio"`
		FormaPago           string  `json:"forma_pago"`
		PlanID              int64   `json:"plan_id"`
		Inicial             float64 `json:"inicial"`
		PrecioPersonalizado float64 `json:"precio_personalizado"`
		ClienteTipoDoc      string  `json:"cliente_tipo_doc"`
		ClienteCedula       string  `json:"cliente_cedula"`
		ClienteNombre       string  `json:"cliente_nombre"`
		ClienteTelefono     string  `json:"cliente_telefono"`
		ClienteActual       string  `json:"cliente_actual"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "Error interpretando datos de la cotización: " + err.Error()
	}

	versions, err := cotizaciones.ObtenerVersiones(ctx, b.supa, emp.SocioComercial)
	if err != nil {
		return "Error cargando catálogo: " + err.Error()
	}
	var ver *cotizaciones.Version
	for i := range versions {
		if versions[i].ID == args.VersionID {
			ver = &versions[i]
			break
		}
	}
	if ver == nil {
		ids := make([]string, 0)
		for i := range versions {
			ids = append(ids, ftoa(versions[i].ID))
		}
		return fmt.Sprintf("No existe la versión %d. Usa listar_catalogo y elige uno de estos ids: %s", args.VersionID, strings.Join(ids[:min(10, len(ids))], ", "))
	}

	tipoPrecio := args.TipoPrecio
	if tipoPrecio == "" {
		tipoPrecio = "estandar"
	}
	formaPago := args.FormaPago
	if !strings.EqualFold(formaPago, "Credito") {
		formaPago = "Contado"
	}

	// Precio a medida solo para administradores.
	custom := args.PrecioPersonalizado
	if custom > 0 && !b.isAdmin(ctx, emp) {
		custom = 0
	}

	cliente, err := b.resolveCliente(ctx, emp, ver, args.ClienteActual, args.ClienteCedula, args.ClienteNombre, args.ClienteTipoDoc, args.ClienteTelefono)
	if err != nil {
		return "No pude resolver el cliente: " + err.Error()
	}

	// Clon de la versión con precio personalizado si aplica.
	v := *ver
	if custom > 0 {
		switch tipoPrecio {
		case "estandar":
			v.PrecioEstandar = custom
		case "premium":
			v.PrecioPremium = custom
		case "flota":
			v.PrecioFlota = custom
		}
	}

	in := cotizaciones.EmitirInput{
		UserID:            emp.UserID,
		ClienteID:         cliente.ID,
		Version:           v,
		TipoPrecio:        tipoPrecio,
		FormaPago:         formaPago,
		NumeroPresupuesto: b.flows.nextNumero(ctx, emp.SocioComercial),
	}

	if formaPago == "Credito" {
		plans, perr := cotizaciones.ObtenerPlanes(ctx, b.supa, emp.SocioComercial)
		if perr != nil {
			return "Error cargando planes: " + perr.Error()
		}
		if args.PlanID == 0 {
			return "Falta el plan_id para Crédito. Planes válidos: " + planesStr(plans)
		}
		in.Plan = cotizaciones.ObtenerPlan(plans, args.PlanID)
		if in.Plan == nil {
			return "El plan " + ftoa(args.PlanID) + " no existe. Planes válidos: " + planesStr(plans)
		}
		precioBase := in.Precio()
		in.Inicial = args.Inicial
		if in.Inicial <= 0 {
			in.Inicial = precioBase * in.Plan.InicialMinimaPorcentaje / 100
		}
		if minInicial := precioBase * in.Plan.InicialMinimaPorcentaje / 100; in.Inicial < minInicial {
			in.Inicial = minInicial
		}
		res, perr := cotizaciones.CalcularPlan(ctx, b.supa, in.Plan.ID, precioBase, in.Inicial)
		if perr != nil {
			return "Error calculando el plan: " + perr.Error()
		}
		in.Resultado = res
	}

	id, err := cotizaciones.EmitirCotizacion(ctx, b.supa, in)
	if err != nil {
		return "Error guardando la cotización: " + err.Error()
	}

	det, err := cotizaciones.ObtenerDetalle(ctx, b.supa, id)
	if err != nil {
		return "Cotización guardada (" + in.NumeroPresupuesto + ") pero no pude generar el comprobante."
	}

	b.sendText(chat, "Cotización "+in.NumeroPresupuesto+" emitida.")
	pdfBytes, perr := pdf.RenderPDF(det)
	if perr != nil {
		pdfBytes, _ = pdf.BuildCotizacion(det)
	}
	if len(pdfBytes) > 0 {
		b.sendMediaQueued(chat, pdfBytes, "application/pdf", in.NumeroPresupuesto+".pdf", false)
	}
	if png, err2 := pdf.RenderPNG(det); err2 == nil && len(png) > 0 {
		b.sendMediaQueued(chat, png, "image/png", in.NumeroPresupuesto+".png", true)
	}

	return fmt.Sprintf("Cotización %s emitida (id %d) para %s. Se envió PDF e imagen.", in.NumeroPresupuesto, id, cliente.NombreRazonSocial)
}

// resolveCliente busca o crea el cliente de la cotización.
func (b *Bot) resolveCliente(ctx context.Context, emp *empleados.Empleado, ver *cotizaciones.Version, term, cedula, nombre, tipoDoc, telefono string) (*cotizaciones.Cliente, error) {
	buscar := term
	if buscar == "" {
		buscar = cedula
	}
	if buscar == "" {
		buscar = nombre
	}
	if buscar != "" {
		cands, err := cotizaciones.BuscarClientes(ctx, b.supa, emp.SocioComercial, buscar)
		if err == nil && len(cands) > 0 {
			c := cands[0]
			return &c, nil
		}
	}
	if cedula == "" || nombre == "" {
		return nil, fmt.Errorf("necesito la cédula y el nombre del cliente para crear uno nuevo")
	}
	id, err := cotizaciones.CrearCliente(ctx, b.supa, emp.SocioComercial, cotizaciones.CrearClienteParams{
		TipoDocumento:    tipoDoc,
		NumeroDocumento:  cedula,
		NombreRazonSocial: nombre,
		TelefonoPrincipal: telefono,
	})
	if err != nil {
		return nil, err
	}
	return &cotizaciones.Cliente{ID: id, SocioComercial: emp.SocioComercial,
		TipoDocumento: tipoDoc, NumeroDocumento: cedula, NombreRazonSocial: nombre,
		TelefonoPrincipal: telefono}, nil
}

func (b *Bot) toolEnviarFicha(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, argsJSON string) string {
	var args struct {
		VersionID           int64   `json:"version_id"`
		TipoPrecio          string  `json:"tipo_precio"`
		PrecioPersonalizado float64 `json:"precio_personalizado"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	versions, err := cotizaciones.ObtenerVersiones(ctx, b.supa, emp.SocioComercial)
	if err != nil {
		return "Error cargando catálogo: " + err.Error()
	}
	for i := range versions {
		if versions[i].ID == args.VersionID {
			custom := args.PrecioPersonalizado
			if custom > 0 && !b.isAdmin(ctx, emp) {
				custom = 0
			}
			png, perr := pdf.RenderVehicleCardPNG(versions[i], args.TipoPrecio, custom)
			if perr != nil {
				return "No pude generar la ficha: " + perr.Error()
			}
			b.sendMediaQueued(chat, png, "image/png", fmt.Sprintf("ficha-%d.png", args.VersionID), true)
			return fmt.Sprintf("Ficha del vehículo %s %s enviada.", versions[i].MarcaNombre, versions[i].ModeloNombre)
		}
	}
	return fmt.Sprintf("No encontré la versión %d.", args.VersionID)
}

// toolImprimirCotizacion imprime (envía PDF + imagen) la cotización indicada
// por su número en el último listado enviado al chat.
func (b *Bot) toolImprimirCotizacion(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, argsJSON string) string {
	var args struct {
		Indice int `json:"indice"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Indice < 1 {
		return "Indica el número de la cotización a imprimir (ej. 'imprime la cotización 1')."
	}
	c, ok := b.flows.resolverCotizacion(ctx, phone, emp, args.Indice)
	if !ok {
		return "No encontré la cotización " + itoa(args.Indice) + " en el listado del mes."
	}
	det, err := cotizaciones.ObtenerDetalle(ctx, b.supa, c.ID)
	if err != nil {
		return "No pude cargar la cotización " + c.NumeroPresupuesto + ": " + err.Error()
	}
	var pdfBytes []byte
	pdfBytes, err = pdf.RenderPDF(det)
	if err != nil {
		pdfBytes, _ = pdf.BuildCotizacion(det)
	}
	if len(pdfBytes) > 0 {
		b.sendMediaQueued(chat, pdfBytes, "application/pdf", c.NumeroPresupuesto+".pdf", false)
	}
	if png, perr := pdf.RenderPNG(det); perr == nil && len(png) > 0 {
		b.sendMediaQueued(chat, png, "image/png", c.NumeroPresupuesto+".png", true)
	}
	return "Cotización " + c.NumeroPresupuesto + " enviada (PDF + imagen)."
}

func (b *Bot) toolRecordar(ctx context.Context, phone, argsJSON string) string {
	var args struct {
		Clave string `json:"clave"`
		Valor string `json:"valor"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Clave == "" {
		return "Dato no válido."
	}
	state, _ := b.state.Get(ctx, phone)
	state[args.Clave] = args.Valor
	if err := b.state.Set(ctx, phone, state); err != nil {
		return "No pude guardar el dato: " + err.Error()
	}
	return fmt.Sprintf("Dato '%s' guardado.", args.Clave)
}

// isAdmin comprueba (con caché) si el empleado tiene el permiso admin_total.
func (b *Bot) isAdmin(ctx context.Context, emp *empleados.Empleado) bool {
	if emp.UserID == "" {
		return false
	}
	b.adminCacheMu.Lock()
	_, ok := b.adminCache[emp.UserID]
	b.adminCacheMu.Unlock()
	if ok {
		return true
	}
	admin := b.queryAdmin(ctx, emp.UserID)
	if admin {
		b.adminCacheMu.Lock()
		b.adminCache[emp.UserID] = struct{}{}
		b.adminCacheMu.Unlock()
	}
	return admin
}

func (b *Bot) queryAdmin(ctx context.Context, userID string) bool {
	var rows []map[string]any
	err := b.supa.RPC(ctx, "obtener_permisos_usuario", map[string]any{"p_user_id": userID}, &rows)
	if err != nil {
		err = b.supa.RPC(ctx, "obtener_permisos_usuario", map[string]any{"uid": userID}, &rows)
		if err != nil {
			return false
		}
	}
	for _, r := range rows {
		for _, key := range []string{"codigo", "codigo_permiso", "permiso", "nombre", "clave"} {
			if v := supabase.GetString(r, key); strings.EqualFold(v, "admin_total") {
				return true
			}
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(strings.TrimSpace(sub)))
}

// planesStr arma la lista legible de planes para informar al modelo.
func planesStr(plans []cotizaciones.Plan) string {
	parts := make([]string, 0, len(plans))
	for _, p := range plans {
		parts = append(parts, fmt.Sprintf("id %d (%s)", p.ID, p.NombrePlan))
	}
	if len(parts) == 0 {
		return "sin planes activos"
	}
	return strings.Join(parts, ", ")
}

func ftoa(v int64) string { return fmt.Sprintf("%d", v) }

func itoa(v int) string { return fmt.Sprintf("%d", v) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
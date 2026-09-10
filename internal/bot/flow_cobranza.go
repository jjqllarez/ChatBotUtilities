package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/cobranzas"
	"bot/internal/empleados"
)

// CobranzaFlow implementa la interfaz Flow para consultar cobranzas:
// resumen general, próximos vencimientos, vencidos, pagados, por cliente
// y por crédito. Es de solo lectura (no modifica datos del CRM).
//
// Estados en bot_chat_state (prefijo cob_):
//   - cob_active (bool): flujo activo.
//   - cob_step (string): "menu" | "dias" | "periodo" | "cliente" | "factura".
type CobranzaFlow struct {
	b *Bot
}

// NewCobranzaFlow crea la instancia de CobranzaFlow.
func NewCobranzaFlow(b *Bot) *CobranzaFlow {
	return &CobranzaFlow{b: b}
}

func (f *CobranzaFlow) Nombre() string { return "cobranza" }
func (f *CobranzaFlow) Tipo() FlowTipo { return FlowAutonomo }

func (f *CobranzaFlow) Activo(ctx context.Context, phone string) bool {
	return f.b.getStateBool(ctx, phone, "cob_active")
}

func (f *CobranzaFlow) Iniciar(phone string, emp *empleados.Empleado) {
	ctx := context.Background()
	f.b.setStateBool(ctx, phone, "cob_active", true)
	f.b.setStateKey(ctx, phone, "cob_step", "menu")
	f.mostrarMenu(types.NewJID(phone, types.DefaultUserServer))
}

func (f *CobranzaFlow) IniciarComo(phone string, emp *empleados.Empleado, _ map[string]any) {
	f.Iniciar(phone, emp)
}

func (f *CobranzaFlow) Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	step, _ := f.b.state.Get(ctx, phone)
	cur, _ := step["cob_step"].(string)
	if cur == "" {
		cur = "menu"
	}
	low := strings.ToLower(strings.TrimSpace(text))

	switch cur {
	case "dias":
		n, ok := parseIndex(text)
		if !ok {
			f.b.sendText(chat, "Escribe cuántos días: 3, 7, 15 o 30.")
			return FlowResult{}, nil
		}
		f.mostrarPorVencer(ctx, chat, emp, n)
		f.volverAlMenu(ctx, phone, chat)
		return FlowResult{}, nil

	case "periodo":
		switch {
		case strings.HasPrefix(low, "1") || strings.Contains(low, "este mes") || strings.Contains(low, "actual"):
			f.mostrarPagados(ctx, chat, emp, 0, "este mes")
		case strings.HasPrefix(low, "2") || strings.Contains(low, "anterior") || strings.Contains(low, "pasado"):
			f.mostrarPagados(ctx, chat, emp, -1, "el mes anterior")
		default:
			f.b.sendText(chat, "Escribe 1 (este mes) o 2 (mes anterior).")
			return FlowResult{}, nil
		}
		f.volverAlMenu(ctx, phone, chat)
		return FlowResult{}, nil

	case "cliente":
		f.mostrarPorCliente(ctx, chat, emp, strings.TrimSpace(text))
		f.volverAlMenu(ctx, phone, chat)
		return FlowResult{}, nil

	case "factura":
		f.mostrarPorCredito(ctx, chat, emp, strings.TrimSpace(text))
		f.volverAlMenu(ctx, phone, chat)
		return FlowResult{}, nil
	}

	// Menú principal.
	if n, ok := parseIndex(text); ok {
		switch n {
		case 1:
			f.mostrarResumen(ctx, chat, emp)
		case 2:
			f.b.setStateKey(ctx, phone, "cob_step", "dias")
			f.b.sendText(chat, "¿En cuántos días? Escribe 3, 7, 15 o 30.")
			return FlowResult{}, nil
		case 3:
			f.mostrarVencidos(ctx, chat, emp)
		case 4:
			f.b.setStateKey(ctx, phone, "cob_step", "periodo")
			f.b.sendText(chat, "¿Qué periodo? Escribe 1 (este mes) o 2 (mes anterior).")
			return FlowResult{}, nil
		case 5:
			f.b.setStateKey(ctx, phone, "cob_step", "cliente")
			f.b.sendText(chat, "Escribe la cédula o el nombre del cliente.")
			return FlowResult{}, nil
		case 6:
			f.b.setStateKey(ctx, phone, "cob_step", "factura")
			f.b.sendText(chat, "Escribe el número de factura (o parte de él).")
			return FlowResult{}, nil
		default:
			f.b.sendText(chat, "Opción no válida. Elige un número del 1 al 6, o /cancelar para salir.")
		}
		f.volverAlMenu(ctx, phone, chat)
		return FlowResult{}, nil
	}

	// No es una opción del menú: que el router/asistente lo atienda.
	return FlowResult{}, errNeedsAssistant
}

func (f *CobranzaFlow) Cancelar(phone string) {
	ctx := context.Background()
	f.b.clearStateKey(ctx, phone, "cob_active")
	f.b.clearStateKey(ctx, phone, "cob_step")
}

func (f *CobranzaFlow) StepHint(ctx context.Context, phone string) string {
	state, _ := f.b.state.Get(ctx, phone)
	step, _ := state["cob_step"].(string)
	switch step {
	case "dias":
		return "El usuario está en el flujo /cobranza y debe elegir cuántos días (3, 7, 15 o 30) para ver los próximos vencimientos."
	case "periodo":
		return "El usuario está en el flujo /cobranza y debe elegir el periodo: 1 (este mes) o 2 (mes anterior)."
	case "cliente":
		return "El usuario está en el flujo /cobranza y debe escribir la cédula o el nombre del cliente a consultar."
	case "factura":
		return "El usuario está en el flujo /cobranza y debe escribir el número de factura a consultar."
	}
	return "El usuario está en el menú /cobranza (consultas de cobranzas). Puede elegir 1-6 o /cancelar."
}

// --- presentación ---

const menuCobranza = "*Cobranzas* — elige una opción:\n" +
	"1) Resumen general\n" +
	"2) Por vencer (3/7/15/30 días)\n" +
	"3) Vencidos\n" +
	"4) Pagados (este mes / mes anterior)\n" +
	"5) Por cliente (cédula o nombre)\n" +
	"6) Por crédito (número de factura)\n" +
	"7) /cancelar para salir"

func (f *CobranzaFlow) mostrarMenu(chat types.JID) {
	f.b.sendText(chat, menuCobranza)
}

func (f *CobranzaFlow) volverAlMenu(ctx context.Context, phone string, chat types.JID) {
	f.b.setStateKey(ctx, phone, "cob_step", "menu")
	f.b.sendText(chat, "Escribe otro número para otra consulta, o /cancelar para salir.")
}

func (f *CobranzaFlow) cargar(ctx context.Context, emp *empleados.Empleado) ([]cobranzas.Cuota, error) {
	return cobranzas.Listar(ctx, f.b.supa, emp.SocioComercial)
}

func (f *CobranzaFlow) mostrarResumen(ctx context.Context, chat types.JID, emp *empleados.Empleado) {
	cuotas, err := f.cargar(ctx, emp)
	if err != nil {
		f.b.sendText(chat, "No pude consultar las cobranzas. Intenta de nuevo.")
		return
	}
	r := cobranzas.Resumir(cuotas)
	var sb strings.Builder
	sb.WriteString("*Resumen de cobranzas*\n")
	fmt.Fprintf(&sb, "Créditos activos: %d\n\n", r.CreditosActivos)
	fmt.Fprintf(&sb, "🔴 Vencidos: %d — USD %s\n", r.VencidosCount, formatQ(r.VencidosMonto))
	fmt.Fprintf(&sb, "🟡 Por vencer: %d — USD %s\n", r.PorVencerCount, formatQ(r.PorVencerMonto))
	if r.ParcialesCount > 0 {
		fmt.Fprintf(&sb, "🟠 Parciales: %d — saldo USD %s\n", r.ParcialesCount, formatQ(r.ParcialesMonto))
	}
	fmt.Fprintf(&sb, "🟢 Pagados: %d — USD %s", r.PagadosCount, formatQ(r.PagadosMonto))
	f.b.sendText(chat, sb.String())
}

func (f *CobranzaFlow) mostrarPorVencer(ctx context.Context, chat types.JID, emp *empleados.Empleado, dias int) {
	cuotas, err := f.cargar(ctx, emp)
	if err != nil {
		f.b.sendText(chat, "No pude consultar las cobranzas. Intenta de nuevo.")
		return
	}
	list := cobranzas.PorVencer(cuotas, dias)
	if len(list) == 0 {
		f.b.sendText(chat, fmt.Sprintf("No hay cuotas por vencer en los próximos %d días.", dias))
		return
	}
	total := 0.0
	for _, c := range list {
		total += c.MontoProgramado
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Por vencer (próximos %d días)* — %d cuotas, USD %s\n\n", dias, len(list), formatQ(total))
	sb.WriteString(formatearCuotas(list, "vence"))
	f.b.sendText(chat, strings.TrimRight(sb.String(), "\n"))
}

func (f *CobranzaFlow) mostrarVencidos(ctx context.Context, chat types.JID, emp *empleados.Empleado) {
	cuotas, err := f.cargar(ctx, emp)
	if err != nil {
		f.b.sendText(chat, "No pude consultar las cobranzas. Intenta de nuevo.")
		return
	}
	list := cobranzas.Vencidos(cuotas)
	if len(list) == 0 {
		f.b.sendText(chat, "No hay cuotas vencidas. 🎉")
		return
	}
	total := 0.0
	for _, c := range list {
		total += c.MontoProgramado - c.MontoPagado
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Cuotas vencidas* — %d cuotas, saldo USD %s\n\n", len(list), formatQ(total))
	sb.WriteString(formatearCuotas(list, "vencido"))
	f.b.sendText(chat, strings.TrimRight(sb.String(), "\n"))
}

func (f *CobranzaFlow) mostrarPagados(ctx context.Context, chat types.JID, emp *empleados.Empleado, offsetMeses int, etiqueta string) {
	cuotas, err := f.cargar(ctx, emp)
	if err != nil {
		f.b.sendText(chat, "No pude consultar las cobranzas. Intenta de nuevo.")
		return
	}
	desde, hasta := cobranzas.RangoMes(offsetMeses)
	list := cobranzas.Pagados(cuotas, desde, hasta)
	if len(list) == 0 {
		f.b.sendText(chat, "No hay pagos registrados en "+etiqueta+".")
		return
	}
	total := 0.0
	for _, c := range list {
		total += c.MontoPagado
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Pagos de %s* — %d cuotas, USD %s\n\n", etiqueta, len(list), formatQ(total))
	sb.WriteString(formatearCuotas(list, "pagado"))
	f.b.sendText(chat, strings.TrimRight(sb.String(), "\n"))
}

func (f *CobranzaFlow) mostrarPorCliente(ctx context.Context, chat types.JID, emp *empleados.Empleado, term string) {
	if term == "" {
		f.b.sendText(chat, "Escribe la cédula o el nombre del cliente.")
		return
	}
	cuotas, err := f.cargar(ctx, emp)
	if err != nil {
		f.b.sendText(chat, "No pude consultar las cobranzas. Intenta de nuevo.")
		return
	}
	list := cobranzas.PorCliente(cuotas, term)
	if len(list) == 0 {
		f.b.sendText(chat, "No encontré cobranzas para '"+term+"'.")
		return
	}
	r := cobranzas.Resumir(list)
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Cliente: %s* (C.I. %s)\n", list[0].ClienteNombre, list[0].ClienteDocumento)
	fmt.Fprintf(&sb, "Vencidos: %d • Por vencer: %d • Pagados: %d\n\n", r.VencidosCount, r.PorVencerCount, r.PagadosCount)
	sb.WriteString(formatearCuotas(list, "detalle"))
	f.b.sendText(chat, strings.TrimRight(sb.String(), "\n"))
}

func (f *CobranzaFlow) mostrarPorCredito(ctx context.Context, chat types.JID, emp *empleados.Empleado, term string) {
	if term == "" {
		f.b.sendText(chat, "Escribe el número de factura.")
		return
	}
	cuotas, err := f.cargar(ctx, emp)
	if err != nil {
		f.b.sendText(chat, "No pude consultar las cobranzas. Intenta de nuevo.")
		return
	}
	list := cobranzas.PorCredito(cuotas, term)
	if len(list) == 0 {
		f.b.sendText(chat, "No encontré cobranzas para la factura '"+term+"'.")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Factura: %s* — %s (C.I. %s)\n\n", list[0].NumeroFactura, list[0].ClienteNombre, list[0].ClienteDocumento)
	sb.WriteString(formatearCuotas(list, "detalle"))
	f.b.sendText(chat, strings.TrimRight(sb.String(), "\n"))
}

// formatearCuotas arma el listado numerado. modo controla la cola mostrada:
// "vence" (fecha), "vencido" (mora), "pagado" (fecha pago), "detalle".
func formatearCuotas(list []cobranzas.Cuota, modo string) string {
	const maxItems = 40
	ref := time.Now()
	if loc, err := time.LoadLocation("America/Caracas"); err == nil {
		ref = time.Now().In(loc)
	}
	ref = time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, ref.Location())

	var sb strings.Builder
	n := len(list)
	shown := list
	if n > maxItems {
		shown = list[:maxItems]
	}
	for i, c := range shown {
		icono := estadoIcono(c.EstadoCuota)
		fmt.Fprintf(&sb, "%d. %s *%s* (C.I. %s)\n", i+1, icono, c.ClienteNombre, c.ClienteDocumento)
		fmt.Fprintf(&sb, "   Factura %s • Giro %s • USD %s", c.NumeroFactura, c.NumeroGiro, formatQ(c.MontoProgramado))
		if c.MontoPagado > 0 && c.EstadoCuota != "Pagado" {
			fmt.Fprintf(&sb, " (pagado USD %s)", formatQ(c.MontoPagado))
		}
		sb.WriteString("\n")
		switch modo {
		case "vencido":
			if d := diasEntre(ref, fechaToTime(c.FechaVencimiento)); d > 0 {
				fmt.Fprintf(&sb, "   Venció %s (%d días de mora)\n", fechaBonita(c.FechaVencimiento), d)
			} else {
				fmt.Fprintf(&sb, "   Venció %s\n", fechaBonita(c.FechaVencimiento))
			}
		case "pagado":
			fmt.Fprintf(&sb, "   Pagado el %s\n", fechaBonita(c.FechaPago))
		default:
			fmt.Fprintf(&sb, "   Vence %s\n", fechaBonita(c.FechaVencimiento))
		}
	}
	if n > maxItems {
		fmt.Fprintf(&sb, "\n(mostrando %d de %d)", maxItems, n)
	}
	return sb.String()
}

func estadoIcono(estado string) string {
	switch estado {
	case "Vencido":
		return "🔴"
	case "Parcial":
		return "🟠"
	case "Pagado":
		return "🟢"
	default:
		return "🟡"
	}
}

// fechaBonita convierte YYYY-MM-DD a DD/MM/YYYY.
func fechaBonita(s string) string {
	if len(s) < 10 {
		return s
	}
	return s[8:10] + "/" + s[5:7] + "/" + s[0:4]
}

// fechaToTime parsea YYYY-MM-DD.
func fechaToTime(s string) time.Time {
	if len(s) < 10 {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return time.Time{}
	}
	return t
}

// diasEntre devuelve los días enteros entre dos fechas (a - b).
func diasEntre(a, b time.Time) int {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	return int(a.Sub(b).Hours() / 24)
}

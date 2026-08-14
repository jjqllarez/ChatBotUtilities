package bot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/cotizaciones"
	"bot/internal/empleados"
)

// reCatalogoId acepta la selección de un vehículo por su ID de versión
// ("Id: 24", "id 24", "el id 24").
var reCatalogoId = regexp.MustCompile(`(?i)\bid\s*:?\s*(\d+)`)

// CatalogoFlow implementa la interfaz Flow para la consulta del catálogo de vehículos.
// Puede ejecutarse de forma autónoma (vendedor pide catálogo) o composable.
//
// Estados (en bot_chat_state, prefijo catalogo_):
//   - catalogo_active (bool): catálogo mostrado; mientras está activo el flujo captura
//     la selección del usuario.
//   - catalogo_ids ([int64]): IDs de la última lista en el mismo orden de numeración.
//   - catalogo_ts (string): marca de tiempo para el TTL de 30 min.
//   - catalogo_sel (int64): versión elegida por el usuario (pendiente de tipo de precio).
//   - catalogo_wait (bool): esperando que el usuario elija el tipo de precio.
//   - catalogo_custom (bool): esperando que el admin teclee el precio personalizado.
type CatalogoFlow struct {
	b *Bot
}

// NewCatalogoFlow crea la instancia de CatalogoFlow.
func NewCatalogoFlow(b *Bot) *CatalogoFlow {
	return &CatalogoFlow{b: b}
}

func (f *CatalogoFlow) Nombre() string { return "catalogo_vehiculos" }
func (f *CatalogoFlow) Tipo() FlowTipo { return FlowComposable }

func (f *CatalogoFlow) Activo(ctx context.Context, phone string) bool {
	state, _ := f.b.state.Get(ctx, phone)
	v, _ := state["catalogo_active"].(bool)
	if !v {
		return false
	}
	// TTL del catálogo: si se inactiva 30 min, se limpia (no dejar estado huérfano).
	if ts, ok := state["catalogo_ts"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil && time.Since(t) > 30*time.Minute {
			f.Cancelar(phone)
			return false
		}
	}
	return true
}

func (f *CatalogoFlow) Iniciar(phone string, emp *empleados.Empleado) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	f.b.sendCatalogo(context.Background(), chat, phone, emp, "")
}

func (f *CatalogoFlow) IniciarComo(phone string, emp *empleados.Empleado, params map[string]any) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	term, _ := params["termino"].(string)
	f.b.sendCatalogo(context.Background(), chat, phone, emp, term)
}

// Procesar interpreta la selección del usuario sobre el catálogo mostrado.
// El flujo tiene dos fases:
//
//  1. Elegir vehículo: "8", "el 8", "Id: 24" -> guarda la versión y pregunta
//     el tipo de precio (1 Estándar, 2 Premium, 3 Flota y, si es admin,
//     4 Precio personalizado).
//  2. Elegir precio: "1"/"2"/"3"/"4" (o el nombre) -> envía la card/ficha con
//     ese tipo de precio; si eligió personalizado, pide el monto y envía la
//     card con el precio a medida.
func (f *CatalogoFlow) Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	low := norm(strings.TrimSpace(text))
	if low == "" {
		return FlowResult{}, nil
	}

	// Fase 2b: esperando el monto del precio personalizado (solo admin).
	if f.b.getStateBool(ctx, phone, "catalogo_custom") {
		return f.handleCustom(ctx, chat, phone, emp, low)
	}

	// Fase 2a: vehículo elegido, esperando el tipo de precio.
	if f.b.getStateBool(ctx, phone, "catalogo_wait") {
		return f.handlePrecio(ctx, chat, phone, emp, low)
	}

	// Prioridad: si quedó una selección pendiente de ficha (varias coincidencias
	// de un término), resolverla primero para no pisar catalogo_ids.
	if len(f.b.getStateIDs(ctx, phone, "ficha_pick")) > 0 &&
		f.b.handleFichaPick(ctx, chat, phone, emp, text) {
		f.Cancelar(phone)
		return FlowResult{Completado: true}, nil
	}

	// Fase 1: "Id: 24" -> versión por ID directo (no índice).
	if m := reCatalogoId.FindStringSubmatch(low); len(m) >= 2 {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		return f.pickVehiculo(ctx, chat, phone, emp, id)
	}

	// Fase 1: "8" / "el 8" / "la 3" -> índice en la última lista mostrada.
	if m := rePickNumber.FindStringSubmatch(low); len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		ids := f.b.getStateIDs(ctx, phone, "catalogo_ids")
		if len(ids) == 0 {
			f.b.sendText(chat, "Primero pide el catálogo (escribe 'catálogo') y luego elige un número.")
			return FlowResult{}, nil
		}
		if n < 1 || n > len(ids) {
			f.b.sendText(chat, fmt.Sprintf("Elige un número entre 1 y %d.", len(ids)))
			return FlowResult{}, nil
		}
		return f.pickVehiculo(ctx, chat, phone, emp, ids[n-1])
	}

	// No es una selección de catálogo: dejar que el router/asistente atienda.
	return FlowResult{}, errNeedsAssistant
}

// pickVehiculo guarda la versión elegida y pregunta el tipo de precio.
func (f *CatalogoFlow) pickVehiculo(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, id int64) (FlowResult, error) {
	if !f.existeVersion(ctx, emp, id) {
		f.b.sendText(chat, fmt.Sprintf("No encontré la versión %d.", id))
		return FlowResult{}, nil
	}
	f.b.setStateInt(ctx, phone, "catalogo_sel", id)
	f.b.setStateBool(ctx, phone, "catalogo_wait", true)
	f.preguntaPrecio(ctx, chat, phone, emp, id)
	return FlowResult{}, nil
}

// handlePrecio interpreta la respuesta al tipo de precio y envía la card.
func (f *CatalogoFlow) handlePrecio(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, low string) (FlowResult, error) {
	sel := f.b.getStateInt(ctx, phone, "catalogo_sel")
	if sel <= 0 {
		f.Cancelar(phone)
		return FlowResult{}, errNeedsAssistant
	}
	tipo := parseTipoPrecio(low)
	switch tipo {
	case "custom":
		if !f.b.isAdmin(ctx, emp) {
			f.b.sendText(chat, "El precio personalizado solo está disponible para administradores. Elige 1, 2 o 3.")
			return FlowResult{}, nil
		}
		f.b.setStateBool(ctx, phone, "catalogo_custom", true)
		f.b.sendText(chat, "Escribe el precio en USD (ej: 25000):")
		return FlowResult{}, nil
	case "":
		f.preguntaPrecio(ctx, chat, phone, emp, sel)
		return FlowResult{}, nil
	default: // estandar / premium / flota
		f.enviaFicha(ctx, chat, phone, emp, sel, tipo, 0)
		f.Cancelar(phone)
		return FlowResult{Completado: true, Datos: map[string]any{"version_id": sel, "tipo_precio": tipo}}, nil
	}
}

// handleCustom interpreta el monto del precio personalizado (admin) y envía la card.
func (f *CatalogoFlow) handleCustom(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, low string) (FlowResult, error) {
	sel := f.b.getStateInt(ctx, phone, "catalogo_sel")
	if sel <= 0 {
		f.Cancelar(phone)
		return FlowResult{}, errNeedsAssistant
	}
	n, ok := firstPositiveNumber(low)
	if !ok {
		f.b.sendText(chat, "Escribe el precio en USD (ej: 25000):")
		return FlowResult{}, nil
	}
	f.enviaFicha(ctx, chat, phone, emp, sel, "", float64(n))
	f.Cancelar(phone)
	return FlowResult{Completado: true, Datos: map[string]any{"version_id": sel, "precio_personalizado": float64(n)}}, nil
}

// preguntaPrecio muestra las opciones de tipo de precio para la versión dada.
func (f *CatalogoFlow) preguntaPrecio(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, id int64) {
	versions, err := cotizaciones.ObtenerVersiones(ctx, f.b.supa, emp.SocioComercial)
	if err != nil {
		f.b.sendText(chat, "No pude cargar el catálogo. Intenta de nuevo.")
		return
	}
	for i := range versions {
		if versions[i].ID == id {
			v := versions[i]
			msg := fmt.Sprintf("¿Qué tipo de precio quieres para la ficha de *%s %s*?\n1) Estándar: %s USD\n2) Premium: %s USD\n3) Flota: %s USD",
				v.MarcaNombre, displayName(v), formatQ(v.PrecioEstandar), formatQ(v.PrecioPremium), formatQ(v.PrecioFlota))
			if f.b.isAdmin(ctx, emp) {
				msg += "\n4) Precio personalizado (teclear el precio)"
			}
			f.b.sendText(chat, msg)
			return
		}
	}
	f.b.sendText(chat, "No encontré ese vehículo.")
}

// existeVersion reporta si el ID de versión pertenece al catálogo del socio.
func (f *CatalogoFlow) existeVersion(ctx context.Context, emp *empleados.Empleado, id int64) bool {
	versions, err := cotizaciones.ObtenerVersiones(ctx, f.b.supa, emp.SocioComercial)
	if err != nil {
		return false
	}
	for i := range versions {
		if versions[i].ID == id {
			return true
		}
	}
	return false
}

// enviaFicha busca la versión por ID y envía su card/ficha con el tipo de
// precio indicado (o precio personalizado si custom > 0). Devuelve false si
// no se encontró (o si la card falló) para que el flujo lo informe.
func (f *CatalogoFlow) enviaFicha(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, id int64, tipoPrecio string, custom float64) bool {
	versions, err := cotizaciones.ObtenerVersiones(ctx, f.b.supa, emp.SocioComercial)
	if err != nil {
		f.b.sendText(chat, "No pude cargar el catálogo. Intenta de nuevo.")
		return false
	}
	for i := range versions {
		if versions[i].ID == id {
			r := f.b.sendFicha(ctx, chat, emp, versions[i], tipoPrecio, custom)
			if directResultError(r) {
				f.b.sendText(chat, r)
			}
			return true
		}
	}
	return false
}

func (f *CatalogoFlow) Cancelar(phone string) {
	ctx := context.Background()
	f.b.clearStateKey(ctx, phone, "catalogo_active")
	f.b.clearStateKey(ctx, phone, "catalogo_ids")
	f.b.clearStateKey(ctx, phone, "catalogo_ts")
	f.b.clearStateKey(ctx, phone, "catalogo_sel")
	f.b.clearStateKey(ctx, phone, "catalogo_wait")
	f.b.clearStateKey(ctx, phone, "catalogo_custom")
}

func (f *CatalogoFlow) StepHint(ctx context.Context, phone string) string {
	if f.b.getStateBool(ctx, phone, "catalogo_custom") {
		return "El usuario eligió un precio personalizado en el catálogo y debe escribir el monto en USD (ej: 25000)."
	}
	if f.b.getStateBool(ctx, phone, "catalogo_wait") {
		return "El usuario eligió un vehículo del catálogo y debe escoger el tipo de precio: 1 (Estándar), 2 (Premium), 3 (Flota) o 4 (Personalizado, solo administradores)."
	}
	return "El usuario pidió el catálogo de vehículos y debe elegir uno escribiendo su número (ej: 3) o su ID (ej: Id: 24)."
}

// parseTipoPrecio interpreta la respuesta del tipo de precio: "1"/"estandar",
// "2"/"premium", "3"/"flota", "4"/"personalizado". Devuelve "" si no coincide.
func parseTipoPrecio(text string) string {
	low := norm(strings.TrimSpace(text))
	if n, ok := firstPositiveNumber(low); ok {
		switch n {
		case 1:
			return "estandar"
		case 2:
			return "premium"
		case 3:
			return "flota"
		case 4:
			return "custom"
		}
	}
	switch {
	case strings.Contains(low, "personalizado"), strings.Contains(low, "a medida"):
		return "custom"
	case strings.Contains(low, "estandar"), strings.Contains(low, "estándar"):
		return "estandar"
	case strings.Contains(low, "premium"):
		return "premium"
	case strings.Contains(low, "flota"):
		return "flota"
	}
	return ""
}

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

// Procesar interpreta la selección del usuario sobre el catálogo mostrado:
//   - "Id: 24" -> envía la ficha de la versión 24 (ID directo).
//   - "8" / "el 8" / "la 3" -> envía la ficha del vehículo N de la última lista.
//   - Cualquier otra cosa -> errNeedsAssistant para que el router/asistente atienda.
func (f *CatalogoFlow) Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	low := norm(strings.TrimSpace(text))
	if low == "" {
		return FlowResult{}, nil
	}

	// Prioridad: si quedó una selección pendiente de ficha (varias coincidencias
	// de un término), resolverla primero para no pisar catalogo_ids.
	if len(f.b.getStateIDs(ctx, phone, "ficha_pick")) > 0 &&
		f.b.handleFichaPick(ctx, chat, phone, emp, text) {
		f.Cancelar(phone)
		return FlowResult{Completado: true}, nil
	}

	// "Id: 24" -> versión por ID directo (no índice).
	if m := reCatalogoId.FindStringSubmatch(low); len(m) >= 2 {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if f.enviaFicha(ctx, chat, phone, emp, id) {
			f.Cancelar(phone)
			return FlowResult{Completado: true, Datos: map[string]any{"version_id": id}}, nil
		}
		f.b.sendText(chat, fmt.Sprintf("No encontré la versión %d.", id))
		return FlowResult{}, nil
	}

	// "8" / "el 8" / "la 3" -> índice en la última lista mostrada.
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
		if f.enviaFicha(ctx, chat, phone, emp, ids[n-1]) {
			f.Cancelar(phone)
			return FlowResult{Completado: true, Datos: map[string]any{"version_id": ids[n-1]}}, nil
		}
		f.b.sendText(chat, "No encontré ese vehículo.")
		return FlowResult{}, nil
	}

	// No es una selección de catálogo: dejar que el router/asistente atienda.
	return FlowResult{}, errNeedsAssistant
}

// enviaFicha busca la versión por ID y envía su card/ficha. Devuelve false si
// no se encontró (o si la card falló) para que el flujo lo informe.
func (f *CatalogoFlow) enviaFicha(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, id int64) bool {
	versions, err := cotizaciones.ObtenerVersiones(ctx, f.b.supa, emp.SocioComercial)
	if err != nil {
		f.b.sendText(chat, "No pude cargar el catálogo. Intenta de nuevo.")
		return false
	}
	for i := range versions {
		if versions[i].ID == id {
			r := f.b.sendFicha(ctx, chat, emp, versions[i], "", 0)
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
}

func (f *CatalogoFlow) StepHint(ctx context.Context, phone string) string {
	return "El usuario pidió el catálogo de vehículos y debe elegir uno escribiendo su número (ej: 3) o su ID (ej: Id: 24)."
}

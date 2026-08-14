package bot

import (
	"context"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/empleados"
)

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
	return v
}

func (f *CatalogoFlow) Iniciar(phone string, emp *empleados.Empleado) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	f.b.sendCatalogo(context.Background(), chat, emp, "")
}

func (f *CatalogoFlow) IniciarComo(phone string, emp *empleados.Empleado, params map[string]any) {
	chat := types.NewJID(phone, types.DefaultUserServer)
	term, _ := params["termino"].(string)
	f.b.sendCatalogo(context.Background(), chat, emp, term)
}

func (f *CatalogoFlow) Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error) {
	// Al ser una respuesta directa, se completa inmediatamente
	return FlowResult{Completado: true}, nil
}

func (f *CatalogoFlow) Cancelar(phone string) {
	ctx := context.Background()
	f.b.clearStateKey(ctx, phone, "catalogo_active")
}

func (f *CatalogoFlow) StepHint(ctx context.Context, phone string) string {
	return ""
}

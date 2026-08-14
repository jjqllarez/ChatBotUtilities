package bot

import (
	"context"

	"bot/internal/empleados"
)

// CotizacionFlow implementa la interfaz Flow para el flujo de creación de
// cotizaciones. Es un envoltorio delgado sobre el flowManager existente;
// toda la lógica interna (pasos, validaciones, Supabase RPCs, PDFs) vive en
// cotizacion_flow.go sin modificar.
//
// Registrado en main.go con: bot.RegisterFlow(bot.NewCotizacionFlow(b))
type CotizacionFlow struct {
	b *Bot
}

// NewCotizacionFlow crea el wrapper del flujo de cotización.
// Requiere que b.flows esté inicializado (se garantiza en Bot.New).
func NewCotizacionFlow(b *Bot) *CotizacionFlow {
	return &CotizacionFlow{b: b}
}

func (f *CotizacionFlow) Nombre() string  { return "cotizacion" }
func (f *CotizacionFlow) Tipo() FlowTipo  { return FlowAutonomo }

func (f *CotizacionFlow) Activo(ctx context.Context, phone string) bool {
	return f.b.flows.active(ctx, phone)
}

func (f *CotizacionFlow) Iniciar(phone string, emp *empleados.Empleado) {
	f.b.flows.start(phone, emp)
}

// IniciarComo para cotizacion es igual a Iniciar — no recibe datos de un padre
// porque cotizacion es siempre el flujo raíz, nunca sub-rutina.
func (f *CotizacionFlow) IniciarComo(phone string, emp *empleados.Empleado, _ map[string]any) {
	f.b.flows.start(phone, emp)
}

func (f *CotizacionFlow) Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error) {
	err := f.b.flows.process(ctx, phone, emp, text)
	if err == errNoFlow {
		return FlowResult{}, nil
	}
	// errNeedsAssistant y otros errores se propagan tal cual para que el
	// dispatcher los maneje igual que antes.
	return FlowResult{Completado: err == nil && !f.b.flows.active(ctx, phone)}, err
}

func (f *CotizacionFlow) Cancelar(phone string) {
	f.b.flows.cancel(phone)
}

func (f *CotizacionFlow) StepHint(ctx context.Context, phone string) string {
	return f.b.flows.stepHint(ctx, phone)
}

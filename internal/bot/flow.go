package bot

import (
	"context"
	"sync"
	"time"

	"bot/internal/empleados"
	"bot/internal/supabase"
)

// ---------------------------------------------------------------------------
// Interfaz Flow — contrato que todo flujo conversacional debe implementar.
// ---------------------------------------------------------------------------

// FlowTipo indica si el flujo puede ser iniciado solo por el usuario (Autonomo)
// o también por otro flujo como subrutina (Composable).
type FlowTipo int

const (
	// FlowAutonomo: el usuario lo inicia directamente. Ej: cotizacion.
	FlowAutonomo FlowTipo = iota
	// FlowComposable: puede iniciarse solo O ser invocado por otro flujo.
	// Ej: catalogo_vehiculos (lo pide el usuario Y lo usa cotizacion para elegir).
	FlowComposable
)

// FlowResult es el resultado que un flujo devuelve al completarse.
// Cuando un flujo es sub-rutina de otro, Datos transporta el resultado
// al flujo padre (ej: version_id elegida, cliente_id creado).
type FlowResult struct {
	Completado bool
	Datos      map[string]any
}

// Flow es la interfaz que todo flujo conversacional debe implementar.
// Para agregar un flujo nuevo: crear un archivo en internal/bot/flows/<nombre>/,
// implementar esta interfaz, y registrarlo con bot.RegisterFlow() en main.go.
// Ver AGENTS.md sección 9 para el protocolo completo.
type Flow interface {
	// Nombre devuelve el identificador único del flujo.
	// Debe coincidir con el campo `nombre` en la tabla bot_flows de Supabase.
	Nombre() string

	// Tipo indica si es autónomo o composable.
	Tipo() FlowTipo

	// Activo reporta si este usuario tiene un borrador de este flujo en curso.
	Activo(ctx context.Context, phone string) bool

	// Iniciar arranca el flujo para este usuario (iniciado por el propio usuario).
	Iniciar(phone string, emp *empleados.Empleado)

	// IniciarComo arranca el flujo como sub-rutina de otro flujo.
	// params contiene datos del flujo padre (ej: contexto parcial de cotización).
	// Solo aplica a FlowComposable; FlowAutonomo puede implementarlo como no-op.
	IniciarComo(phone string, emp *empleados.Empleado, params map[string]any)

	// Procesar maneja el siguiente mensaje del usuario dentro del flujo.
	// Devuelve FlowResult con Completado=true cuando el flujo terminó.
	// Si es sub-rutina, FlowResult.Datos contiene el resultado para el padre.
	Procesar(ctx context.Context, phone string, emp *empleados.Empleado, text string) (FlowResult, error)

	// Cancelar aborta el flujo y limpia todo su estado en Supabase.
	// Debe responder a /cancelar sin dejar estado huérfano.
	Cancelar(phone string)

	// StepHint devuelve una descripción del paso actual para el prompt del LLM
	// de conversación (usado en runAssistantV2). Puede devolver "" si no aplica.
	StepHint(ctx context.Context, phone string) string
}

// ---------------------------------------------------------------------------
// FlowRegistry — registro de flujos y caché de bot_flows desde Supabase.
// ---------------------------------------------------------------------------

// botFlowMeta es la representación de una fila de la tabla bot_flows.
type botFlowMeta struct {
	ID             int      `json:"id"`
	SocioComercial int      `json:"socio_comercial"`
	Nombre         string   `json:"nombre"`
	Descripcion    string   `json:"descripcion"`
	FrasesEjemplo  []string `json:"frases_ejemplo"`
	Activo         bool     `json:"activo"`
	Orden          int      `json:"orden"`
}

// FlowRegistry mantiene la lista de flujos registrados y el caché de metadatos
// de Supabase para el LLM pre-clasificador.
type FlowRegistry struct {
	supa  *supabase.Client
	mu    sync.RWMutex
	flows []Flow

	// Caché de bot_flows para el LLM pre-clasificador.
	metaMu      sync.RWMutex
	meta        []botFlowMeta
	metaLoadedAt time.Time
	metaTTL     time.Duration
}

// newFlowRegistry crea un registro vacío. Los flujos se registran con Register().
func newFlowRegistry(supa *supabase.Client) *FlowRegistry {
	return &FlowRegistry{
		supa:    supa,
		metaTTL: 30 * time.Minute,
	}
}

// Register agrega un flujo al registro. Se llama desde main.go al arrancar el bot.
func (r *FlowRegistry) Register(f Flow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flows = append(r.flows, f)
}

// ActiveFlow devuelve el flujo que está activo para este usuario, o nil si ninguno.
func (r *FlowRegistry) ActiveFlow(ctx context.Context, phone string) Flow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, f := range r.flows {
		if f.Activo(ctx, phone) {
			return f
		}
	}
	return nil
}

// FindByName busca un flujo registrado por su nombre.
func (r *FlowRegistry) FindByName(nombre string) Flow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, f := range r.flows {
		if f.Nombre() == nombre {
			return f
		}
	}
	return nil
}

// LoadMeta carga (o devuelve del caché) los metadatos de bot_flows para el
// LLM pre-clasificador. El caché se refresca cada metaTTL (30 min por defecto).
func (r *FlowRegistry) LoadMeta(ctx context.Context, socioComercial int) []botFlowMeta {
	r.metaMu.RLock()
	if time.Since(r.metaLoadedAt) < r.metaTTL && len(r.meta) > 0 {
		cached := r.meta
		r.metaMu.RUnlock()
		return cached
	}
	r.metaMu.RUnlock()

	// Recargar desde Supabase.
	rows, err := r.supa.Select(ctx, "bot_flows",
		"?select=id,socio_comercial,nombre,descripcion,frases_ejemplo,activo,orden"+
			"&socio_comercial=eq."+itoa(socioComercial)+
			"&activo=eq.true&order=orden",
	)
	if err != nil {
		// Si falla la carga, devolver caché anterior (puede estar vacío).
		r.metaMu.RLock()
		old := r.meta
		r.metaMu.RUnlock()
		return old
	}

	fresh := make([]botFlowMeta, 0, len(rows))
	for _, row := range rows {
		m := botFlowMeta{
			Nombre:      supabase.GetString(row, "nombre"),
			Descripcion: supabase.GetString(row, "descripcion"),
			Activo:      supabase.GetBool(row, "activo"),
		}
		// frases_ejemplo llega como []interface{} desde JSON.
		if arr, ok := row["frases_ejemplo"].([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					m.FrasesEjemplo = append(m.FrasesEjemplo, s)
				}
			}
		}
		fresh = append(fresh, m)
	}

	r.metaMu.Lock()
	r.meta = fresh
	r.metaLoadedAt = time.Now()
	r.metaMu.Unlock()

	return fresh
}

package cotizaciones

import (
	"context"
	"encoding/json"

	"bot/internal/supabase"
)

// Plan es un plan de financiamiento (su cálculo lo hace la Edge Function motorjson).
type Plan struct {
	ID                      int64           `json:"id"`
	EnteFinancieroID        *int64          `json:"ente_financiero_id"`
	NombrePlan              string          `json:"nombre_plan"`
	ConfiguracionCalculo    json.RawMessage `json:"configuracion_calculo"`
	AplicaVehiculosNuevos   bool            `json:"aplica_vehiculos_nuevos"`
	AplicaVehiculosUsados   bool            `json:"aplica_vehiculos_usados"`
	InicialMinimaPorcentaje float64         `json:"inicial_minima_porcentaje"`
	Activo                  bool            `json:"activo"`
}

// ResultadoMotor es la respuesta de la Edge Function motorjson.
type ResultadoMotor struct {
	Tokens         json.RawMessage   `json:"tokens"`
	Bloques        []json.RawMessage `json:"bloques"`
	OrdenEjecucion []string          `json:"orden_ejecucion"`
}

// PlanContadoID es el id del plan hardcodeado para la forma de pago Contado.
const PlanContadoID = 25

// ObtenerPlanes lista los planes de financiamiento activos para el socio.
func ObtenerPlanes(ctx context.Context, client *supabase.Client, socioID int64) ([]Plan, error) {
	rows, err := client.Select(ctx, "planes_financiamiento",
		"?select=id,ente_financiero_id,nombre_plan,configuracion_calculo,aplica_vehiculos_nuevos,aplica_vehiculos_usados,inicial_minima_porcentaje,activo&activo=eq.true&order=id")
	if err != nil {
		return nil, err
	}
	plans := make([]Plan, 0, len(rows))
	for _, r := range rows {
		p := Plan{
			ID:                      supabase.GetInt(r, "id"),
			NombrePlan:              supabase.GetString(r, "nombre_plan"),
			AplicaVehiculosNuevos:   supabase.GetBool(r, "aplica_vehiculos_nuevos"),
			AplicaVehiculosUsados:   supabase.GetBool(r, "aplica_vehiculos_usados"),
			InicialMinimaPorcentaje: supabase.GetFloat(r, "inicial_minima_porcentaje"),
			Activo:                  supabase.GetBool(r, "activo"),
		}
		if cfg := r["configuracion_calculo"]; cfg != nil {
			if b, err := json.Marshal(cfg); err == nil {
				p.ConfiguracionCalculo = b
			}
		}
		if ef := r["ente_financiero_id"]; ef != nil {
			v := supabase.GetInt(r, "ente_financiero_id")
			p.EnteFinancieroID = &v
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// ObtenerPlan encuentra un plan por id.
func ObtenerPlan(plans []Plan, id int64) *Plan {
	for i := range plans {
		if plans[i].ID == id {
			return &plans[i]
		}
	}
	return nil
}

// CalcularPlan llama a la Edge Function motorjson para un plan.
func CalcularPlan(ctx context.Context, client *supabase.Client, planID int64, precioBase, inicial float64) (*ResultadoMotor, error) {
	var out ResultadoMotor
	err := client.EdgeFunction(ctx, "motorjson", map[string]any{
		"plan_id": planID,
		"variables_entrada": map[string]float64{
			"precio_base": precioBase,
			"inicial":     inicial,
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

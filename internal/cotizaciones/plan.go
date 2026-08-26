package cotizaciones

import (
	"context"
	"encoding/json"
	"fmt"

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
	Tablas         []json.RawMessage `json:"tablas"`
	OrdenEjecucion []string          `json:"orden_ejecucion"`
}

// PlanContadoID es el id del plan hardcodeado para la forma de pago Contado.
const PlanContadoID = 25

// tokenConfig representa un token individual dentro de configuracion_calculo.
type tokenConfig struct {
	Tipo    string          `json:"tipo"`
	Token   string          `json:"token"`
	Nombre  string          `json:"nombre"`
	Formato string          `json:"formato"`
	Formula string          `json:"formula"`
	Prefijo string          `json:"prefijo"`
	Sufijo  string          `json:"sufijo"`
}

// TokenEditable es un token que se muestra al usuario para edición.
type TokenEditable struct {
	Token   string  // ID del token (ej: "seguro")
	Nombre  string  // Nombre legible
	Valor   float64 // Valor por defecto (parseado de formula)
	Formato string  // "Moneda", "Porcentaje", "Entero", "Crudo"
	Prefijo string  // "$", etc.
	Sufijo  string  // "meses", etc.
}

// configuracionCalculo es la estructura parcial de configuracion_calculo.
type configuracionCalculo struct {
	Tokens []tokenConfig `json:"tokens"`
}

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
// variables es un map de token→valor que se envía como variables_entrada.
func CalcularPlan(ctx context.Context, client *supabase.Client, planID int64, variables map[string]float64) (*ResultadoMotor, error) {
	var out ResultadoMotor
	err := client.EdgeFunction(ctx, "motorjson", map[string]any{
		"plan_id":           planID,
		"variables_entrada": variables,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// EditableTokens extrae los tokens editables de un plan.
// Solo devuelve tokens de tipo "Variable" (los que el empleado puede cambiar).
// Excluye "Calculado" (resultado del motor) y "Constante" (fijos del plan).
func EditableTokens(plan *Plan) []TokenEditable {
	if plan == nil || len(plan.ConfiguracionCalculo) == 0 {
		return nil
	}
	var cfg configuracionCalculo
	if err := json.Unmarshal(plan.ConfiguracionCalculo, &cfg); err != nil {
		return nil
	}
	var out []TokenEditable
	for _, t := range cfg.Tokens {
		if t.Tipo != "Variable" {
			continue
		}
		valor := parseTokenFormula(t.Formula)
		out = append(out, TokenEditable{
			Token:   t.Token,
			Nombre:  t.Nombre,
			Valor:   valor,
			Formato: t.Formato,
			Prefijo: t.Prefijo,
			Sufijo:  t.Sufijo,
		})
	}
	return out
}

// parseTokenFormula extrae un valor numérico de la formula de un token.
// Si la formula es un número simple (ej: "1330", "0.0226"), lo devuelve.
// Si es una fórmula con @tokens, devuelve 0 (se calculará en motorjson).
func parseTokenFormula(formula string) float64 {
	if formula == "" {
		return 0
	}
	// Si contiene @, es una fórmula calculada — devolver 0
	for i := 0; i < len(formula); i++ {
		if formula[i] == '@' {
			return 0
		}
	}
	var v float64
	_, err := fmt.Sscanf(formula, "%f", &v)
	if err != nil {
		return 0
	}
	return v
}

package cotizaciones

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"bot/internal/supabase"
)

// CotizacionBreve es una fila resumida del listado de cotizaciones.
type CotizacionBreve struct {
	ID                int64   `json:"id"`
	NumeroPresupuesto string  `json:"numero_presupuesto"`
	FormaPago         string  `json:"forma_pago"`
	PrecioVehiculo    float64 `json:"precio_vehiculo"`
	Estado            string  `json:"estado"`
	CreatedAt         string  `json:"created_at"`
	Cliente           string
}

// ListarCotizaciones devuelve las cotizaciones más recientes de un socio.
func ListarCotizaciones(ctx context.Context, client *supabase.Client, socioID int64, limit int) ([]CotizacionBreve, error) {
	return listarEntre(ctx, client, socioID, 0, "", "", limit)
}

// ListarCotizacionesMes devuelve las cotizaciones del mes en curso (hora de
// Venezuela) de un socio. Si empleadoID > 0, solo las emitidas por ese empleado.
func ListarCotizacionesMes(ctx context.Context, client *supabase.Client, socioID, empleadoID int64, limit int) ([]CotizacionBreve, error) {
	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	inicio := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).UTC().Format(time.RFC3339)
	fin := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, loc).UTC().Format(time.RFC3339)
	return listarEntre(ctx, client, socioID, empleadoID, inicio, fin, limit)
}

func listarEntre(ctx context.Context, client *supabase.Client, socioID, empleadoID int64, desde, hasta string, limit int) ([]CotizacionBreve, error) {
	query := fmt.Sprintf("?select=id,numero_presupuesto,forma_pago,precio_vehiculo,estado,created_at,empleado_id,clientes(nombre_razon_social)&socio_comercial_id=eq.%d&order=created_at.desc&limit=%d", socioID, limit)
	if empleadoID > 0 {
		query += "&empleado_id=eq." + fmt.Sprintf("%d", empleadoID)
	}
	if desde != "" {
		query += "&created_at=gte." + url.QueryEscape(desde)
	}
	if hasta != "" {
		query += "&created_at=lt." + url.QueryEscape(hasta)
	}
	rows, err := client.Select(ctx, "cotizaciones", query)
	if err != nil {
		return nil, err
	}
	out := make([]CotizacionBreve, 0, len(rows))
	for _, r := range rows {
		item := CotizacionBreve{
			ID:                supabase.GetInt(r, "id"),
			NumeroPresupuesto: supabase.GetString(r, "numero_presupuesto"),
			FormaPago:         supabase.GetString(r, "forma_pago"),
			PrecioVehiculo:    supabase.GetFloat(r, "precio_vehiculo"),
			Estado:            supabase.GetString(r, "estado"),
			CreatedAt:         supabase.GetString(r, "created_at"),
		}
		item.Cliente = firstEmbeddedName(r["clientes"])
		out = append(out, item)
	}
	return out, nil
}

func firstEmbeddedName(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["nombre_razon_social"].(string); ok {
			return s
		}
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	if m, ok := arr[0].(map[string]any); ok {
		if s, ok := m["nombre_razon_social"].(string); ok {
			return s
		}
	}
	return ""
}

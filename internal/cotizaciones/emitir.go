package cotizaciones

import (
	"context"
	"encoding/json"
	"fmt"

	"bot/internal/supabase"
)

// EmitirInput agrupa los datos necesarios para emitir (guardar) una cotización.
type EmitirInput struct {
	UserID            string
	ClienteID         int64
	Version           Version
	TipoPrecio        string // premium | flota | estandar
	FormaPago         string // Contado | Credito
	Inicial           float64
	NumeroPresupuesto string
	Plan              *Plan
	Resultado         *ResultadoMotor
}

// PrecioVenta es el precio según el tipo elegido.
func (in EmitirInput) Precio() float64 { return in.Version.PrecioPorTipo(in.TipoPrecio) }

// buildDetalle arma el JSONB detalle_cotizacion (igual estructura que la web).
func (in EmitirInput) buildDetalle() map[string]any {
	precio := in.Precio()
	porcentaje := 0.0
	if precio > 0 {
		porcentaje = in.Inicial / precio * 100
	}

	vehiculo := map[string]any{
		"id":                in.Version.ID,
		"nombre_version":    in.Version.NombreVersion,
		"tipo":              in.Version.Tipo,
		"clase":             in.Version.Clase,
		"uso":               in.Version.Uso,
		"tipo_motorizacion": in.Version.TipoMotorizacion,
		"marca_id":          in.Version.MarcaID,
		"marca_nombre":      in.Version.MarcaNombre,
		"modelo_id":         in.Version.ModeloID,
		"modelo_nombre":     in.Version.ModeloNombre,
		"imagen_venta":      in.Version.ImagenVenta,
		"precio_id":         in.Version.PrecioID,
		"precio_flota":      in.Version.PrecioFlota,
		"precio_estandar":   in.Version.PrecioEstandar,
		"precio_premium":    in.Version.PrecioPremium,
	}

	detalle := map[string]any{
		"vehiculo":            vehiculo,
		"personalizacion":     map[string]any{},
		"tipo_precio":         in.TipoPrecio,
		"inicial":             in.Inicial,
		"porcentaje_inicial":  porcentaje,
		"precio_seleccionado": precio,
	}

	if in.Plan != nil {
		plan := map[string]any{
			"id":                        in.Plan.ID,
			"nombre_plan":               in.Plan.NombrePlan,
			"inicial_minima_porcentaje": in.Plan.InicialMinimaPorcentaje,
		}
		if in.Resultado != nil {
			plan["resultado_motor"] = in.Resultado
		}
		detalle["plan"] = plan
	}
	return detalle
}

// EmitirCotizacion guarda una cotización vía el RPC puente.
func EmitirCotizacion(ctx context.Context, client *supabase.Client, in EmitirInput) (int64, error) {
	var res []struct {
		ID int64 `json:"id"`
	}
	detalle, err := json.Marshal(in.buildDetalle())
	if err != nil {
		return 0, err
	}
	err = client.RPC(ctx, "insertar_cotizacion_empleado", map[string]any{
		"p_user_id":            in.UserID,
		"p_cliente_id":         in.ClienteID,
		"p_version_id":         in.Version.ID,
		"p_numero_presupuesto": in.NumeroPresupuesto,
		"p_forma_pago":         in.FormaPago,
		"p_precio_vehiculo":    in.Precio(),
		"p_estado":             "Borrador",
		"p_inf_opcional":       map[string]any{},
		"p_detalle_cotizacion": json.RawMessage(detalle),
	}, &res)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("el RPC no devolvió el id de la cotización")
	}
	return res[0].ID, nil
}

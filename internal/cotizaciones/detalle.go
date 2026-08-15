package cotizaciones

import (
	"context"

	"bot/internal/supabase"
)

// Detalle es la respuesta completa de obtener_detalle_cotizacion.
type Detalle struct {
	ID                int64             `json:"id"`
	Estado            string            `json:"estado"`
	NumeroPresupuesto string            `json:"numero_presupuesto"`
	FechaEmision      string            `json:"fecha_emision"`
	FormaPago         string            `json:"forma_pago"`
	Vendedor          string            `json:"vendedor"`
	Cliente           ClienteDetalle    `json:"cliente"`
	SocioComercial    Socio             `json:"socio_comercial"`
	Detalle           DetalleCotizacion `json:"detalle_cotizacion"`
}

type ClienteDetalle struct {
	ID       int64  `json:"id"`
	Nombre   string `json:"nombre"`
	Cedula   string `json:"cedula"`
	Email    string `json:"email"`
	Telefono string `json:"telefono"`
}

type Socio struct {
	RazonSocial string `json:"razon_social"`
	RIF         string `json:"rif"`
	Telefono    string `json:"telefono"`
	Correo      string `json:"correo"`
	Direccion   string `json:"direccion"`
	LogoURL     string `json:"logo_url"`
}

type DetalleCotizacion struct {
	Vehiculo           Version        `json:"vehiculo"`
	Personalizacion    map[string]any `json:"personalizacion"`
	TipoPrecio         string         `json:"tipo_precio"`
	PrecioSeleccionado float64        `json:"precio_seleccionado"`
	Inicial            float64        `json:"inicial"`
	PorcentajeInicial  float64        `json:"porcentaje_inicial"`
	Plan               PlanDetalle    `json:"plan"`
}

type PlanDetalle struct {
	ID         int64  `json:"id"`
	NombrePlan string `json:"nombre_plan"`
	Resultado  struct {
		Bloques []Bloque `json:"bloques"`
		Tablas  []Tabla  `json:"tablas"`
	} `json:"resultado_motor"`
}

type Bloque struct {
	Nombre        string   `json:"nombre"`
	TextoCompleto string   `json:"texto_completo"`
	Lineas        []string `json:"lineas"`
}

type Tabla struct {
	Nombre   string     `json:"nombre"`
	Columnas []string   `json:"columnas"`
	Filas    [][]string `json:"filas"`
}

// ObtenerDetalle estructurado de una cotización guardada.
func ObtenerDetalle(ctx context.Context, client *supabase.Client, cotizacionID int64) (*Detalle, error) {
	var d Detalle
	err := client.RPC(ctx, "obtener_detalle_cotizacion", map[string]any{
		"p_cotizacion_id": cotizacionID,
	}, &d)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

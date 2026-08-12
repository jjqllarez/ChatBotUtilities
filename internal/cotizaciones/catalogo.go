package cotizaciones

import (
	"context"
	"encoding/json"

	"bot/internal/supabase"
)

// Version es una versión de vehículo con su precio vigente (3 tipos).
type Version struct {
	ID               int64           `json:"id"`
	NombreVersion    string          `json:"nombre_version"`
	Tipo             string          `json:"tipo"`
	Clase            string          `json:"clase"`
	Uso              string          `json:"uso"`
	TipoMotorizacion string          `json:"tipo_motorizacion"`
	MarcaID          int64           `json:"marca_id"`
	MarcaNombre      string          `json:"marca_nombre"`
	ModeloID         int64           `json:"modelo_id"`
	ModeloNombre     string          `json:"modelo_nombre"`
	ImagenVenta      string          `json:"imagen_venta"`
	PrecioID         int64           `json:"precio_id"`
	PrecioFlota      float64         `json:"precio_flota"`
	PrecioEstandar   float64         `json:"precio_estandar"`
	PrecioPremium    float64         `json:"precio_premium"`
	Personalizacion  json.RawMessage `json:"personalizacion"`
}

// Cliente es un cliente buscado o creado.
type Cliente struct {
	ID                int64  `json:"id"`
	SocioComercial    int64  `json:"socio_comercial_id"`
	TipoDocumento     string `json:"tipo_documento"`
	NumeroDocumento   string `json:"numero_documento"`
	NombreRazonSocial string `json:"nombre_razon_social"`
	Correo            string `json:"correo"`
	TelefonoPrincipal string `json:"telefono_principal"`
	Direccion         string `json:"direccion"`
	Activo            bool   `json:"activo"`
}

// PrecioCorresponde devuelve el valor del precio según el tipo dado.
func (v Version) PrecioPorTipo(tipo string) float64 {
	switch tipo {
	case "flota":
		return v.PrecioFlota
	case "estandar":
		return v.PrecioEstandar
	default:
		return v.PrecioPremium
	}
}

// ObtenerVersiones lista las versiones con precio vigente para un socio comercial.
func ObtenerVersiones(ctx context.Context, client *supabase.Client, socioID int64) ([]Version, error) {
	var out []Version
	err := client.RPC(ctx, "obtener_versiones_completas", map[string]any{
		"p_socio_comercial_id": socioID,
	}, &out)
	return out, err
}

// BuscarClientes busca clientes por término (cédula/nombre).
func BuscarClientes(ctx context.Context, client *supabase.Client, socioID int64, term string) ([]Cliente, error) {
	var out []Cliente
	err := client.RPC(ctx, "buscar_clientes", map[string]any{
		"p_search_term":        term,
		"p_socio_comercial_id": socioID,
	}, &out)
	return out, err
}

// CrearCliente crea un cliente nuevo y devuelve su id.
func CrearCliente(ctx context.Context, client *supabase.Client, socioID int64, p CrearClienteParams) (int64, error) {
	var res []struct {
		ID int64 `json:"id"`
	}
	err := client.RPC(ctx, "insertar_cliente_empleado", map[string]any{
		"p_socio_comercial_id":  socioID,
		"p_tipo_documento":      p.TipoDocumento,
		"p_numero_documento":    p.NumeroDocumento,
		"p_nombre_razon_social": p.NombreRazonSocial,
		"p_correo":              p.Correo,
		"p_telefono_principal":  p.TelefonoPrincipal,
		"p_direccion":           p.Direccion,
	}, &res)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0].ID, nil
}

// CrearClienteParams son los datos para crear un cliente.
type CrearClienteParams struct {
	TipoDocumento     string
	NumeroDocumento   string
	NombreRazonSocial string
	Correo            string
	TelefonoPrincipal string
	Direccion         string
}

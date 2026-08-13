package cotizaciones

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"bot/internal/supabase"
)

var reDocLetra = regexp.MustCompile(`(?i)^\s*[VEJPG]\s*-?\s*(\d{4,})\s*$`)

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

// BuscarClientes busca clientes por término (cédula/nombre). Si el término
// trae la letra del documento ("V-16573081") y la búsqueda no encuentra nada,
// reintenta con solo los dígitos ("16573081"), porque el RPC busca contra
// numero_documento sin la letra.
func BuscarClientes(ctx context.Context, client *supabase.Client, socioID int64, term string) ([]Cliente, error) {
	term = strings.TrimSpace(term)
	var out []Cliente
	err := client.RPC(ctx, "buscar_clientes", map[string]any{
		"p_search_term":        term,
		"p_socio_comercial_id": socioID,
	}, &out)
	if err != nil {
		return out, err
	}
	if len(out) > 0 {
		return out, nil
	}
	// Término con letra de documento + número → reintentar sin la letra.
	if digits := docDigits(term); digits != "" && digits != term {
		var out2 []Cliente
		if err2 := client.RPC(ctx, "buscar_clientes", map[string]any{
			"p_search_term":        digits,
			"p_socio_comercial_id": socioID,
		}, &out2); err2 == nil && len(out2) > 0 {
			return out2, nil
		}
	}
	return out, nil
}

// docDigits extrae los dígitos de un documento con letra ("V-16573081",
// "E 12345678"); devuelve "" si el texto no es un documento con letra.
func docDigits(term string) string {
	m := reDocLetra.FindStringSubmatch(term)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// BuscarClientePorDocumento busca el cliente exacto por socio + tipo de
// documento + número de cédula (para resolver duplicados al crear).
func BuscarClientePorDocumento(ctx context.Context, client *supabase.Client, socioID int64, tipoDoc, cedula string) (*Cliente, error) {
	if cedula == "" {
		return nil, nil
	}
	rows, err := client.Select(ctx, "clientes",
		fmt.Sprintf("?select=*&socio_comercial_id=eq.%d&tipo_documento=eq.%s&numero_documento=eq.%s&limit=1",
			socioID, url.QueryEscape(tipoDoc), url.QueryEscape(cedula)))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	c, err := clienteFromMap(rows[0])
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CrearCliente crea un cliente nuevo y devuelve su id.
func CrearCliente(ctx context.Context, client *supabase.Client, socioID int64, p CrearClienteParams) (int64, error) {
	var raw json.RawMessage
	err := client.RPC(ctx, "insertar_cliente_empleado", map[string]any{
		"p_socio_comercial_id":  socioID,
		"p_tipo_documento":      p.TipoDocumento,
		"p_numero_documento":    p.NumeroDocumento,
		"p_nombre_razon_social": p.NombreRazonSocial,
		"p_correo":              p.Correo,
		"p_telefono_principal":  p.TelefonoPrincipal,
		"p_direccion":           p.Direccion,
	}, &raw)
	if err != nil {
		// El RPC puede fallar con 409 si el cliente ya existe (unique
		// unq_cliente_por_socio): resolver el duplicado y devolver el id.
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "23505") {
			if p.TipoDocumento != "" && p.NumeroDocumento != "" {
				ex, xerr := BuscarClientePorDocumento(ctx, client, socioID, p.TipoDocumento, p.NumeroDocumento)
				if xerr == nil && ex != nil {
					return ex.ID, nil
				}
			}
		}
		return 0, err
	}
	// El RPC devuelve un objeto {"id": N, ...}; se acepta también un array por robustez.
	var one struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &one) == nil && one.ID != 0 {
		return one.ID, nil
	}
	var many []struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
		return many[0].ID, nil
	}
	return 0, nil
}

// clienteFromMap convierte una fila REST de clientes en un Cliente.
func clienteFromMap(r map[string]any) (Cliente, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return Cliente{}, err
	}
	var c Cliente
	if err := json.Unmarshal(data, &c); err != nil {
		return Cliente{}, err
	}
	return c, nil
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

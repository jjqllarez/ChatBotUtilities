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
// Robustez: si el RPC falla (red/timeout) o no devuelve resultados, se hace un
// fallback con SELECT directo por documento normalizado y por nombre (ilike),
// de modo que la búsqueda solo falla si no hay ningún candidato local.
func BuscarClientes(ctx context.Context, client *supabase.Client, socioID int64, term string) ([]Cliente, error) {
	term = strings.TrimSpace(term)
	var out []Cliente
	rpcErr := client.RPC(ctx, "buscar_clientes", map[string]any{
		"p_search_term":        term,
		"p_socio_comercial_id": socioID,
	}, &out)
	if rpcErr == nil && len(out) > 0 {
		return out, nil
	}
	// Fallback 1: documento con letra -> reintentar con solo los dígitos.
	if rpcErr == nil {
		if digits := docDigits(term); digits != "" && digits != term {
			var out2 []Cliente
			if err2 := client.RPC(ctx, "buscar_clientes", map[string]any{
				"p_search_term":        digits,
				"p_socio_comercial_id": socioID,
			}, &out2); err2 == nil && len(out2) > 0 {
				return out2, nil
			}
		}
	}
	// Fallback 2: SELECT directo por documento normalizado.
	if digits := normalizeDoc(term); digits != "" {
		rows, derr := client.Select(ctx, "clientes",
			fmt.Sprintf("?select=*&socio_comercial_id=eq.%d&numero_documento=eq.%s&limit=10",
				socioID, url.QueryEscape(digits)))
		if derr == nil {
			for _, r := range rows {
				if c, cerr := clienteFromMap(r); cerr == nil {
					out = append(out, c)
				}
			}
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	// Fallback 3: SELECT directo por nombre (contiene el término).
	if rows, serr := client.Select(ctx, "clientes",
		fmt.Sprintf("?select=*&socio_comercial_id=eq.%d&nombre_razon_social=ilike.*%s*&limit=10",
			socioID, url.QueryEscape(term))); serr == nil {
		for _, r := range rows {
			if c, cerr := clienteFromMap(r); cerr == nil {
				out = append(out, c)
			}
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	// Fallback 4: SELECT directo por teléfono (si el cliente ya existe por
	// telefono_principal, encontrarlo antes de intentar registrarlo como nuevo).
	if phone := phoneDigits(term); phone != "" {
		rows, perr := client.Select(ctx, "clientes",
			fmt.Sprintf("?select=*&socio_comercial_id=eq.%d&telefono_principal=eq.%s&limit=5",
				socioID, url.QueryEscape(phone)))
		if perr == nil {
			for _, r := range rows {
				if c, cerr := clienteFromMap(r); cerr == nil {
					out = append(out, c)
				}
			}
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	// Solo se propaga el error del RPC si no hubo ningún fallback exitoso.
	return out, rpcErr
}

// phoneDigits deja solo los dígitos de un teléfono ("04141234567" o
// "+58 414-1234567"); devuelve "" si no hay suficientes dígitos.
func phoneDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) >= 7 {
		return digits
	}
	return ""
}

// normalizeDoc deja solo los dígitos de un documento ("V-014205368",
// "E 12345678" o "14205368") eliminando letra, espacios y ceros a la izquierda.
func normalizeDoc(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "0")
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
// Robustez: prueba el documento tal cual y su versión normalizada, y si no
// hay coincidencia con el tipo exacto, acepta el primer cliente con ese
// documento dentro del socio.
func BuscarClientePorDocumento(ctx context.Context, client *supabase.Client, socioID int64, tipoDoc, cedula string) (*Cliente, error) {
	tipoDoc = strings.ToUpper(strings.TrimSpace(tipoDoc))
	if cedula == "" {
		return nil, nil
	}
	cands := []string{strings.TrimSpace(cedula), normalizeDoc(cedula)}
	// 1) Con tipo de documento exacto.
	for _, cand := range cands {
		if cand == "" {
			continue
		}
		q := fmt.Sprintf("?select=*&socio_comercial_id=eq.%d&numero_documento=eq.%s&limit=5",
			socioID, url.QueryEscape(cand))
		if tipoDoc != "" {
			q += "&tipo_documento=eq." + url.QueryEscape(tipoDoc)
		}
		rows, err := client.Select(ctx, "clientes", q)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			c, cerr := clienteFromMap(r)
			if cerr != nil {
				continue
			}
			if tipoDoc != "" && strings.EqualFold(c.TipoDocumento, tipoDoc) {
				return &c, nil
			}
		}
	}
	// 2) Sin tipo: aceptar el primer cliente del socio con ese documento
	//    (resuelve duplicados por tipo, p. ej. E- vs V-).
	for _, cand := range cands {
		if cand == "" {
			continue
		}
		rows, err := client.Select(ctx, "clientes",
			fmt.Sprintf("?select=*&socio_comercial_id=eq.%d&numero_documento=eq.%s&limit=1",
				socioID, url.QueryEscape(cand)))
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		c, cerr := clienteFromMap(rows[0])
		if cerr != nil {
			return nil, cerr
		}
		return &c, nil
	}
	return nil, nil
}

// CrearCliente crea un cliente nuevo y devuelve su id.
// Robustez: normaliza tipo/documento, verifica duplicado por documento ANTES
// de insertar (evita el 409 y los duplicados), reintenta 2 veces ante errores
// transitorios y, si el RPC responde 409 igualmente, resuelve el duplicado.
func CrearCliente(ctx context.Context, client *supabase.Client, socioID int64, p CrearClienteParams) (int64, error) {
	p.TipoDocumento = strings.ToUpper(strings.TrimSpace(p.TipoDocumento))
	p.NumeroDocumento = strings.TrimSpace(p.NumeroDocumento)
	p.NombreRazonSocial = strings.TrimSpace(p.NombreRazonSocial)
	if p.TipoDocumento != "" && p.NumeroDocumento != "" {
		if ex, err := BuscarClientePorDocumento(ctx, client, socioID, p.TipoDocumento, p.NumeroDocumento); err == nil && ex != nil {
			return ex.ID, nil
		}
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
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
		if err == nil {
			// El RPC devuelve un objeto {"id": N, ...}; se acepta también un
			// array por robustez.
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
		lastErr = err
		// El RPC puede fallar con 409 si el cliente ya existe (unique
		// unq_cliente_por_socio): resolver el duplicado y devolver el id.
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "23505") {
			if p.TipoDocumento != "" && p.NumeroDocumento != "" {
				ex, xerr := BuscarClientePorDocumento(ctx, client, socioID, p.TipoDocumento, p.NumeroDocumento)
				if xerr == nil && ex != nil {
					return ex.ID, nil
				}
			}
			return 0, err
		}
	}
	return 0, lastErr
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

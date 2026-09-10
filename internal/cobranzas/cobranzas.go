package cobranzas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"bot/internal/supabase"
)

// Cuota es una cuota/giro del cronograma con los datos del crédito y cliente.
type Cuota struct {
	ID               int64
	CreditoID        int64
	NumeroGiro       string
	MontoProgramado  float64
	FechaVencimiento string // YYYY-MM-DD
	FechaPago        string // YYYY-MM-DD o ""
	MontoPagado      float64
	EstadoCuota      string // Pendiente | Pagado | Parcial | Vencido
	NumeroFactura    string
	EstadoCredito    string
	ClienteNombre    string
	ClienteDocumento string
	ClienteTelefono  string
}

// Resumen son los totales generales de cobranzas.
type Resumen struct {
	PorVencerCount  int
	PorVencerMonto  float64
	VencidosCount   int
	VencidosMonto   float64
	PagadosCount    int
	PagadosMonto    float64
	ParcialesCount  int
	ParcialesMonto  float64
	CreditosActivos int
}

// cuotaRaw es la fila cruda que devuelve PostgREST con el embedding anidado
// (cuotas_giros -> creditos -> clientes).
type cuotaRaw struct {
	ID               int64   `json:"id"`
	NumeroGiro       string  `json:"numero_giro"`
	MontoProgramado  float64 `json:"monto_programado"`
	FechaVencimiento string  `json:"fecha_vencimiento"`
	FechaPago        string  `json:"fecha_pago"`
	MontoPagado      float64 `json:"monto_pagado"`
	EstadoCuota      string  `json:"estado_cuota"`
	Creditos         struct {
		ID            int64  `json:"id"`
		NumeroFactura string `json:"numero_factura"`
		EstadoCredito string `json:"estado_credito"`
		ClienteID     int64  `json:"cliente_id"`
		Clientes      struct {
			NombreRazonSocial string `json:"nombre_razon_social"`
			NumeroDocumento   string `json:"numero_documento"`
			TelefonoPrincipal string `json:"telefono_principal"`
		} `json:"clientes"`
	} `json:"creditos"`
}

// Listar trae todas las cuotas de los créditos activos de un socio comercial.
func Listar(ctx context.Context, client *supabase.Client, socioID int64) ([]Cuota, error) {
	q := "?select=id,numero_giro,monto_programado,fecha_vencimiento,fecha_pago,monto_pagado,estado_cuota," +
		"creditos!inner(id,numero_factura,estado_credito,cliente_id," +
		"clientes(nombre_razon_social,numero_documento,telefono_principal))" +
		"&creditos.socio_comercial_id=eq." + url.QueryEscape(fmt.Sprintf("%d", socioID)) +
		"&creditos.activo=eq.true&order=fecha_vencimiento"
	rows, err := client.Select(ctx, "cuotas_giros", q)
	if err != nil {
		return nil, err
	}
	out := make([]Cuota, 0, len(rows))
	for _, r := range rows {
		data, err := json.Marshal(r)
		if err != nil {
			continue
		}
		var cr cuotaRaw
		if err := json.Unmarshal(data, &cr); err != nil {
			continue
		}
		out = append(out, Cuota{
			ID:               cr.ID,
			CreditoID:        cr.Creditos.ID,
			NumeroGiro:       cr.NumeroGiro,
			MontoProgramado:  cr.MontoProgramado,
			FechaVencimiento: cr.FechaVencimiento,
			FechaPago:        cr.FechaPago,
			MontoPagado:      cr.MontoPagado,
			EstadoCuota:      cr.EstadoCuota,
			NumeroFactura:    cr.Creditos.NumeroFactura,
			EstadoCredito:    cr.Creditos.EstadoCredito,
			ClienteNombre:    cr.Creditos.Clientes.NombreRazonSocial,
			ClienteDocumento: cr.Creditos.Clientes.NumeroDocumento,
			ClienteTelefono:  cr.Creditos.Clientes.TelefonoPrincipal,
		})
	}
	return out, nil
}

// hoy devuelve la fecha actual en hora Venezuela como "YYYY-MM-DD".
func hoy() string {
	loc, err := time.LoadLocation("America/Caracas")
	if err != nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	return now.Format("2006-01-02")
}

// parseFecha parsea "YYYY-MM-DD"; devuelve "" si no es válida.
func parseFecha(s string) string {
	if len(s) < 10 {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s[:10]); err != nil {
		return ""
	}
	return s[:10]
}

// esVencida indica si la cuota está vencida (marcada Vencido, o Pendiente con
// fecha ya pasada). Las fechas se comparan como ISO (YYYY-MM-DD) para evitar
// problemas de zona horaria.
func esVencida(c Cuota, ref string) bool {
	if c.EstadoCuota == "Vencido" {
		return true
	}
	if c.EstadoCuota == "Pendiente" {
		f := parseFecha(c.FechaVencimiento)
		return f != "" && f < ref
	}
	return false
}

// Resumir calcula los totales generales.
func Resumir(cuotas []Cuota) Resumen {
	ref := hoy()
	var r Resumen
	creditos := map[int64]bool{}
	for _, c := range cuotas {
		creditos[c.CreditoID] = true
		switch {
		case c.EstadoCuota == "Pagado":
			r.PagadosCount++
			r.PagadosMonto += c.MontoPagado
		case c.EstadoCuota == "Parcial":
			r.ParcialesCount++
			r.ParcialesMonto += c.MontoProgramado - c.MontoPagado
		case esVencida(c, ref):
			r.VencidosCount++
			r.VencidosMonto += c.MontoProgramado - c.MontoPagado
		default: // Pendiente por vencer
			r.PorVencerCount++
			r.PorVencerMonto += c.MontoProgramado
		}
	}
	r.CreditosActivos = len(creditos)
	return r
}

// PorVencer devuelve las cuotas Pendiente que vencen dentro de los próximos
// `dias` días (incluyendo hoy). dias <= 0 usa 30 por defecto.
func PorVencer(cuotas []Cuota, dias int) []Cuota {
	if dias <= 0 {
		dias = 30
	}
	ref := hoy()
	refT, _ := time.Parse("2006-01-02", ref)
	limite := refT.AddDate(0, 0, dias).Format("2006-01-02")
	var out []Cuota
	for _, c := range cuotas {
		if c.EstadoCuota != "Pendiente" {
			continue
		}
		f := parseFecha(c.FechaVencimiento)
		if f == "" {
			continue
		}
		if f >= ref && f <= limite {
			out = append(out, c)
		}
	}
	return out
}

// Vencidos devuelve las cuotas vencidas (marcadas Vencido o Pendiente pasadas).
func Vencidos(cuotas []Cuota) []Cuota {
	ref := hoy()
	var out []Cuota
	for _, c := range cuotas {
		if esVencida(c, ref) {
			out = append(out, c)
		}
	}
	return out
}

// Pagados devuelve las cuotas pagadas o parciales con fecha_pago en el rango
// [desde, hasta] (inclusive). Ambas fechas en formato YYYY-MM-DD.
func Pagados(cuotas []Cuota, desde, hasta string) []Cuota {
	d := parseFecha(desde)
	h := parseFecha(hasta)
	var out []Cuota
	for _, c := range cuotas {
		if c.EstadoCuota != "Pagado" && c.EstadoCuota != "Parcial" {
			continue
		}
		f := parseFecha(c.FechaPago)
		if f == "" {
			continue
		}
		if d != "" && f < d {
			continue
		}
		if h != "" && f > h {
			continue
		}
		out = append(out, c)
	}
	return out
}

// PorCliente filtra por nombre o documento del cliente (contiene, sin acentos).
func PorCliente(cuotas []Cuota, term string) []Cuota {
	t := norm(term)
	if t == "" {
		return nil
	}
	var out []Cuota
	for _, c := range cuotas {
		if strings.Contains(norm(c.ClienteNombre), t) || strings.Contains(norm(c.ClienteDocumento), t) {
			out = append(out, c)
		}
	}
	return out
}

// PorCredito filtra por número de factura (contiene).
func PorCredito(cuotas []Cuota, term string) []Cuota {
	t := norm(term)
	if t == "" {
		return nil
	}
	var out []Cuota
	for _, c := range cuotas {
		if strings.Contains(norm(c.NumeroFactura), t) {
			out = append(out, c)
		}
	}
	return out
}

// RangoMes devuelve (desde, hasta) del mes de referencia (primer y último día).
// offsetMeses = 0 -> mes actual; -1 -> mes anterior.
func RangoMes(offsetMeses int) (string, string) {
	ref, err := time.Parse("2006-01-02", hoy())
	if err != nil {
		ref = time.Now()
	}
	primero := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location()).AddDate(0, offsetMeses, 0)
	ultimo := primero.AddDate(0, 1, -1)
	return primero.Format("2006-01-02"), ultimo.Format("2006-01-02")
}

// norm normaliza texto para búsquedas: minúsculas y sin acentos.
func norm(s string) string {
	r := strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
		"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N",
	)
	return strings.ToLower(strings.TrimSpace(r.Replace(s)))
}

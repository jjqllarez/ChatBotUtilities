package pdf

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"bot/internal/cotizaciones"
)

const (
	condiciones = "Planchart Global, C.A. es el Distribuidor Maestro Oficial de DONGFENG en Venezuela. " +
		"Capital Motors forma parte de la red autorizada de concesionarios afiliados para comercialización, " +
		"entrega y servicio postventa de la marca."
)

// BuildCotizacion genera el PDF (hoja Carta) de una cotización guardada.
func BuildCotizacion(d *cotizaciones.Detalle) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(10, 10, 10)
	pdf.SetAutoPageBreak(true, 28)
	pdf.AddPage()

	drawHeader(pdf, d)
	drawClienteYAuto(pdf, d)
	drawVehiculo(pdf, d)
	drawBloques(pdf, d.Detalle.Plan)
	pdf.Ln(4)
	drawCondiciones(pdf)
	drawFirmas(pdf)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawHeader(pdf *fpdf.Fpdf, d *cotizaciones.Detalle) {
	s := d.SocioComercial

	// Logo (izquierda)
	if img, format, ok := fetchImage(s.LogoURL); ok {
		var r io.Reader
		var name string
		switch format {
		case "png":
			r = bytes.NewReader(img)
			name = "logo.png"
		case "jpeg":
			r = bytes.NewReader(img)
			name = "logo.jpg"
		}
		if r != nil {
			if err := pdf.RegisterImageReader(name, format, r); err == nil {
				pdf.ImageOptions(name, 10, 10, 45, 0, false, fpdf.ImageOptions{ImageType: format}, 0, "")
			}
		}
	}

	// Empresa (centro)
	razon := s.RazonSocial
	if razon == "" {
		razon = "CAPITAL MOTORS, C.A"
	}
	pdf.SetFont("Arial", "B", 15)
	pdf.SetXY(58, 12)
	pdf.Cell(95, 8, razon)

	sub := "R.I.F.: " + orEmpty(s.RIF)
	if s.Direccion != "" {
		sub += "  " + s.Direccion
	}
	if s.Telefono != "" {
		sub += "  TLF. " + s.Telefono
	}
	if s.Correo != "" {
		sub += "  " + s.Correo
	}
	pdf.SetFont("Arial", "", 7)
	pdf.SetXY(58, 20)
	pdf.MultiCell(95, 3, sub, "", "C", false)

	// Cabecera derecha (presupuesto)
	pdf.SetFont("Arial", "B", 9)
	pdf.SetXY(156, 12)
	pdf.SetTextColor(0, 51, 102)
	pdf.MultiCell(44, 4, "PRESUPUESTO\nN°: "+orEmpty(d.NumeroPresupuesto), "", "R", false)
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Arial", "", 8)
	pdf.SetXY(156, 24)
	pdf.MultiCell(44, 4, "Fecha Emisión: "+fechaCorta(d.FechaEmision)+"\nForma de Pago: "+orEmpty(d.FormaPago), "", "R", false)

	pdf.Ln(30)
}

func drawClienteYAuto(pdf *fpdf.Fpdf, d *cotizaciones.Detalle) {
	c := d.Cliente
	// Caja cliente (izquierda)
	x, y := 10.0, pdf.GetY()
	w, h := 130.0, 34.0
	pdf.SetXY(x, y)
	pdf.SetFont("Arial", "", 9)
	lines := []string{
		"Cliente: " + c.Nombre,
		"R.I.F/C.I: " + c.Cedula,
		"e-mail: " + c.Email,
		"Teléfonos: " + c.Telefono,
		"Vendedor: " + orEmpty(d.Vendedor, "No asignado"),
	}
	pdf.Rect(x, y, w, h, "D")
	pdf.SetXY(x+2, y+1)
	for i, ln := range lines {
		pdf.SetXY(x+2, y+2+float64(i)*6)
		pdf.Cell(w-4, 5, ln)
	}

	// Imagen vehículo (derecha)
	if img, format, ok := fetchImage(d.Detalle.Vehiculo.ImagenVenta); ok {
		if err := pdf.RegisterImageReader("auto."+format, format, bytes.NewReader(img)); err == nil {
			pdf.ImageOptions("auto."+format, x+w+4, y, 60, 34, false, fpdf.ImageOptions{ImageType: format}, 0, "")
		}
	} else {
		pdf.SetXY(x+w+4, y+12)
		pdf.SetFont("Arial", "", 8)
		pdf.Cell(60, 6, "IMAGEN AUTO")
	}
	pdf.SetY(y + h + 4)
}

func drawVehiculo(pdf *fpdf.Fpdf, d *cotizaciones.Detalle) {
	v := d.Detalle.Vehiculo
	pdf.SetFillColor(209, 209, 209)
	pdf.SetFont("Arial", "B", 10)
	pdf.CellFormat(0, 7, "DATOS DEL VEHICULO", "1", 1, "L", true, 0, "")

	per := "Ninguna"
	if len(d.Detalle.Personalizacion) > 0 {
		var parts []string
		for k, val := range d.Detalle.Personalizacion {
			parts = append(parts, fmt.Sprintf("%v: %v", k, val))
		}
		per = strings.Join(parts, " | ")
	}
	rows := [][2]string{
		{"Marca", v.MarcaNombre},
		{"Modelo", v.ModeloNombre},
		{"Versión", v.NombreVersion},
		{"Tipo", v.Tipo},
		{"Motorización", v.TipoMotorizacion},
		{"Personalización", per},
	}
	rowH, colW := 6.0, 45.0
	for _, r := range rows {
		pdf.SetFont("Arial", "B", 9)
		pdf.CellFormat(colW, rowH, r[0], "1", 0, "L", false, 0, "")
		pdf.SetFont("Arial", "", 9)
		pdf.CellFormat(0, rowH, r[1], "1", 1, "L", false, 0, "")
	}
}

func drawBloques(pdf *fpdf.Fpdf, plan cotizaciones.PlanDetalle) {
	for _, b := range plan.Resultado.Bloques {
		nombre := strings.TrimRight(b.Nombre, ".")
		if nombre == "" {
			nombre = "Información"
		}
		pdf.Ln(2)
		pdf.SetFillColor(209, 209, 209)
		pdf.SetFont("Arial", "B", 10)
		pdf.CellFormat(0, 6, nombre, "1", 1, "L", true, 0, "")

		y := pdf.GetY()
		h := 5.0 * float64(len(b.Lineas))
		if h < 8 {
			h = 8
		}
		pdf.Rect(pdf.GetX(), y, 196, h, "D")
		pdf.SetFont("Arial", "", 9)
		for i, ln := range b.Lineas {
			pdf.SetXY(12, y+1+float64(i)*5)
			pdf.Cell(192, 5, ln)
		}
		pdf.SetY(y + h)
	}
}

func drawCondiciones(pdf *fpdf.Fpdf) {
	pdf.Ln(2)
	pdf.SetDrawColor(0, 51, 102)
	pdf.SetLineWidth(0.3)
	pdf.Line(10, pdf.GetY(), 206, pdf.GetY())
	pdf.SetFont("Arial", "", 8)
	pdf.SetX(10)
	pdf.MultiCell(196, 3.4, condiciones, "", "J", false)
}

func drawFirmas(pdf *fpdf.Fpdf) {
	pdf.Ln(10)
	fy := pdf.GetY()
	w := 80.0
	x1, x2 := 10.0, 206-80.0
	pdf.SetFont("Arial", "", 9)
	pdf.Line(x1, fy, x1+w, fy)
	pdf.SetXY(x1, fy+1)
	pdf.Cell(w, 5, "Asesor Comercial")
	pdf.Line(x2, fy, x2+w, fy)
	pdf.SetXY(x2, fy+1)
	pdf.Cell(w, 5, "Cliente")
}

// fetchImage descarga una imagen por URL en modo best-effort.
func fetchImage(url string) ([]byte, string, bool) {
	if url == "" {
		return nil, "", false
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(data) < 8 {
		return nil, "", false
	}
	format := detectImageFormat(data, url)
	if format == "" {
		return nil, "", false
	}
	return data, format, true
}

func detectImageFormat(data []byte, url string) string {
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}
	l := strings.ToLower(url)
	if strings.Contains(l, ".png") {
		return "png"
	}
	if strings.Contains(l, ".jpg") || strings.Contains(l, ".jpeg") {
		return "jpeg"
	}
	return ""
}

func fechaCorta(iso string) string {
	if iso == "" {
		return time.Now().Format("02/01/2006")
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.Format("02/01/2006")
}

func orEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

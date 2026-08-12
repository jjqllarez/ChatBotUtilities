package pdf

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"bot/internal/cotizaciones"
)

// RenderPDF genera el PDF idéntico al CRM renderizando el web component real
// con Chromium headless (on-demand). Devuelve los bytes del PDF.
//
// El binario de Chrome/Chromium se toma de CHROME_PATH o se busca
// chromium_headless_shell dentro de ms-playwright (dev). Si no se puede
// renderizar por navegador, el llamante debe hacer fallback a fpdf.
func RenderPDF(d *cotizaciones.Detalle) ([]byte, error) {
	html, err := buildHTML(d)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "cotpdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	htmlPath := filepath.Join(tmp, "hoja.html")
	pdfPath := filepath.Join(tmp, "salida.pdf")
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		return nil, err
	}

	chrome := chromePath()
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--no-pdf-header-footer",
		"--virtual-time-budget=4000",
		"--print-to-pdf=" + pdfPath,
		"file:///" + filepath.ToSlash(htmlPath),
	}
	cmd := exec.Command(chrome, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("chrome render: %v: %s", err, strings.TrimSpace(out.String()))
	}

	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return nil, fmt.Errorf("chrome no produjo un PDF válido (%d bytes)", len(data))
	}
	return data, nil
}

// RenderPNG genera una imagen (PNG) de la hoja de cotización, útil como vista
// previa para enviar directo al cliente sin abrir el documento.
func RenderPNG(d *cotizaciones.Detalle) ([]byte, error) {
	html, err := buildHTML(d)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "cotpng-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	htmlPath := filepath.Join(tmp, "hoja.html")
	pngPath := filepath.Join(tmp, "vista.png")
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		return nil, err
	}

	chrome := chromePath()
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--hide-scrollbars",
		"--force-device-scale-factor=2",
		"--window-size=816,1056",
		"--virtual-time-budget=4000",
		"--screenshot=" + pngPath,
		"file:///" + filepath.ToSlash(htmlPath),
	}
	cmd := exec.Command(chrome, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("chrome screenshot: %v: %s", err, strings.TrimSpace(out.String()))
	}

	data, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		return nil, fmt.Errorf("chrome no produjo un PNG válido (%d bytes)", len(data))
	}
	return data, nil
}

// buildHTML arma la hoja HTML que inyecta el web component real con los datos
// de la cotización (mismo mecanismo que listar.astro).
func buildHTML(d *cotizaciones.Detalle) (string, error) {
	comp, err := os.ReadFile(assetPath("capital-motors-cotizacion.js"))
	if err != nil {
		return "", fmt.Errorf("leyendo web component: %w", err)
	}
	// Logo del componente es "/dongfeng.png" (hardcodeado): lo reemplazamos por
	// un data URI para que funcione desde file:// sin servir el asset.
	if png, err := os.ReadFile(assetPath("dongfeng.png")); err == nil {
		dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		comp = bytes.ReplaceAll(comp, []byte(`"/dongfeng.png"`), []byte(`"`+dataURI+`"`))
	}

	datos, err := json.Marshal(componentData(d))
	if err != nil {
		return "", err
	}

	html := `<!doctype html><html><head><meta charset="utf-8">
<style>
  html,body{margin:0;padding:0}
  @page{size:Letter;margin:0}
</style></head>
<body>
<capital-motors-cotizacion id="wc"></capital-motors-cotizacion>
<script>` + string(comp) + `</script>
<script>
  (function(){
    var data = ` + string(datos) + `;
    var wc = document.getElementById('wc');
    wc.clienteNombre = data.cliente.nombre;
    wc.clienteCi = data.cliente.cedula;
    wc.clienteEmail = data.cliente.email;
    wc.clienteTelefono = data.cliente.telefono;
    wc.clienteVendedor = data.vendedor;
    wc.socioComercial = data.socio_comercial;
    wc.numeroPresupuesto = data.numero_presupuesto;
    wc.formaPago = data.forma_pago;
    wc.fechaEmision = data.fecha_emision;
    wc.imagenAuto = data.imagen_auto;
    wc.datosVehiculo = data.datos_vehiculo;
    wc.bloques = data.bloques;
    window.__renderReady = true;
  })();
</script>
</body></html>`
	return html, nil
}

// componentData arma el objeto de datos que consume el web component.
func componentData(d *cotizaciones.Detalle) map[string]any {
	det := d.Detalle
	cli := d.Cliente
	soc := d.SocioComercial

	per := "Ninguna"
	if len(det.Personalizacion) > 0 {
		parts := make([]string, 0, len(det.Personalizacion))
		for k, v := range det.Personalizacion {
			parts = append(parts, fmt.Sprintf("%v: %v", k, v))
		}
		per = strings.Join(parts, " | ")
	}

	var bloques []cotizaciones.Bloque
	if det.Plan.ID != 0 {
		bloques = det.Plan.Resultado.Bloques
	}

	return map[string]any{
		"cliente":            cli,
		"vendedor":           orEmpty(d.Vendedor, "No asignado"),
		"socio_comercial":    soc,
		"numero_presupuesto": d.NumeroPresupuesto,
		"forma_pago":         d.FormaPago,
		"fecha_emision":      fechaCorta(d.FechaEmision),
		"imagen_auto":        det.Vehiculo.ImagenVenta,
		"datos_vehiculo": map[string]any{
			"Marca":           orEmpty(det.Vehiculo.MarcaNombre, "N/A"),
			"Modelo":          orEmpty(det.Vehiculo.ModeloNombre, "N/A"),
			"Versión":         orEmpty(det.Vehiculo.NombreVersion, "N/A"),
			"Tipo":            orEmpty(det.Vehiculo.Tipo, "N/A"),
			"Motorización":    orEmpty(det.Vehiculo.TipoMotorizacion, "N/A"),
			"Personalización": per,
		},
		"bloques": bloques,
	}
}

// chromePath resuelve el binario de Chromium a usar.
func chromePath() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	// Dev: chromium_headless_shell de ms-playwright
	if p, ok := findHeadlessShell(); ok {
		return p
	}
	return "chrome"
}

func findHeadlessShell() (string, bool) {
	if runtime.GOOS != "windows" {
		return "", false
	}
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "ms-playwright")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return "", false
	}
	for _, dir := range dirs {
		if !dir.IsDir() || !strings.HasPrefix(dir.Name(), "chromium_headless_shell-") {
			continue
		}
		exe := filepath.Join(base, dir.Name(), "chrome-headless-shell-win64", "chrome-headless-shell.exe")
		if fileExists(exe) {
			return exe, true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// assetPath resuelve un asset (web component, logo) relativo al proyecto.
// Busca desde el directorio de trabajo hacia arriba por la carpeta assets/.
func assetPath(name string) string {
	if dir, err := os.Getwd(); err == nil {
		for d := dir; ; d = filepath.Dir(d) {
			cand := filepath.Join(d, "assets", name)
			if fileExists(cand) {
				return cand
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "assets", name)
		if fileExists(p) {
			return p
		}
	}
	return filepath.Join("assets", name)
}

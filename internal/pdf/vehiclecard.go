package pdf

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bot/internal/cotizaciones"
)

// RenderVehicleCardPNG genera una imagen (PNG) de la ficha/card del vehículo
// (web component vehicle-card-pro, sin el botón "Consultar"), útil para enviar
// un precio rápido directo al cliente.
//
// `tipoPrecio` selecciona el precio de lista (premium/flota/estandar) a menos
// que `precioCustom > 0`, en cuyo caso se muestra ese precio a medida.
func RenderVehicleCardPNG(v cotizaciones.Version, tipoPrecio string, precioCustom float64) ([]byte, error) {
	html, err := buildCardHTML(v, tipoPrecio, precioCustom)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "card-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	htmlPath := filepath.Join(tmp, "card.html")
	pngPath := filepath.Join(tmp, "card.png")
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
		"--window-size=440,600",
		"--virtual-time-budget=4000",
		"--screenshot=" + pngPath,
		"file:///" + filepath.ToSlash(htmlPath),
	}
	cmd := exec.Command(chrome, args...)
	cmd.Env = append(os.Environ(), "HOME="+tmp, "XDG_CONFIG_HOME="+tmp)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("chrome card screenshot: %v: %s", err, strings.TrimSpace(out.String()))
	}

	data, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		return nil, fmt.Errorf("chrome no produjo un PNG válido (%d bytes)", len(data))
	}
	// Recortar al tamaño exacto de la card (sin fondo alrededor).
	return trimPNG(data)
}

// trimPNG recorta la imagen eliminando el fondo uniforme alrededor del contenido.
func trimPNG(data []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	// Color de fondo: esquina superior izquierda de la captura.
	bgR, bgG, bgB, _ := img.At(b.Min.X, b.Min.Y).RGBA()
	bgR, bgG, bgB = bgR>>8, bgG>>8, bgB>>8

	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, _ := img.At(x, y).RGBA()
			r, g, bb = r>>8, g>>8, bb>>8
			if absDiff(r, bgR) > 10 || absDiff(g, bgG) > 10 || absDiff(bb, bgB) > 10 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
				found = true
			}
		}
	}
	if !found {
		return data, nil
	}

	// Margen mínimo para conservar el borde de la card.
	margin := 2
	if minX > margin {
		minX -= margin
	}
	if minY > margin {
		minY -= margin
	}
	if maxX+margin < b.Max.X {
		maxX += margin
	}
	if maxY+margin < b.Max.Y {
		maxY += margin
	}

	w, h := maxX-minX+1, maxY-minY+1
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, y, img.At(minX+x, minY+y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

func buildCardHTML(v cotizaciones.Version, tipoPrecio string, precioCustom float64) (string, error) {
	comp, err := os.ReadFile(assetPath("vehicle-card.js"))
	if err != nil {
		return "", fmt.Errorf("leyendo vehicle-card.js: %w", err)
	}
	compStr := string(comp)
	// Quitar el botón "Consultar" (no se desea en el envío directo al cliente).
	compStr = strings.ReplaceAll(compStr, `<button class="cta">Consultar</button>`, "")

	precio := precioCustom
	if precio <= 0 {
		precio = v.PrecioPorTipo(tipoPrecio)
	}
	html := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">
<style>
  html,body{margin:0;padding:0;height:100%%}
  body{display:grid;place-items:center;font-family:system-ui;background:#0f172a}
</style></head>
<body>
<vehicle-card-pro id="card"
  marca="%s" modelo="%s" version="%s" tipo="%s" motorizacion="%s"
  precio="%s" moneda="USD" imagen="%s"></vehicle-card-pro>
<script>%s</script>
</body></html>`,
		escAttr(v.MarcaNombre), escAttr(v.ModeloNombre), escAttr(v.NombreVersion),
		escAttr(v.Tipo), escAttr(v.TipoMotorizacion), formatVE(precio), escAttr(v.ImagenVenta),
		compStr)
	return html, nil
}

func escAttr(s string) string {
	return html.EscapeString(s)
}

// formatVE formatea un número al estilo de Venezuela: 33424.52 -> "33.424,52".
func formatVE(v float64) string {
	s := strings.ReplaceAll(fmt.Sprintf("%.2f", v), ".", ",")
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	parts := strings.Split(s, ",")
	intPart := parts[0]
	var b strings.Builder
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(".")
		}
		b.WriteByte(intPart[i])
	}
	out := b.String()
	if len(parts) > 1 {
		out += "," + parts[1]
	}
	if neg {
		return "-" + out
	}
	return out
}

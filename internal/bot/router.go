package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/cotizaciones"
	"bot/internal/empleados"
	"bot/internal/pdf"
)

// intent es el resultado de la clasificación determinista del mensaje.
type intent int

const (
	intentConversacion intent = iota
	intentCrearCotizacion
	intentFichaVehiculo
	intentImprimirCotizacion
	intentCatalogo
	intentListarCotizaciones
	intentAyudaCotizar
)

// rePickNumber acepta la respuesta de selección de una lista (número suelto o
// "el 2", "la 2", "numero 2").
var rePickNumber = regexp.MustCompile(`^(?:(?:el|la|numero|número)\s*)?(\d{1,3})\s*$`)

// fichaMarkers son los marcadores que indican pedir la ficha/foto de un
// vehículo (fuertes = inequívocos; débiles = necesitan contexto vehicular).
var fichaMarkers = []string{
	"ficha", "card", "tarjeta", "foto", "imagen",
	"ver el", "ver la", "ver",
	"muestra el", "muestra la", "muestra", "muestrame", "muéstrame",
	"enseña", "ensena", "dame", "pasa", "manda", "mandame", "manda",
	"envia", "enviame", "envíame", "mostrar",
}

var reFichaFuerte = regexp.MustCompile(`(?i)(ficha|card|tarjeta|foto|imagen)`)
var reFichaDebil = regexp.MustCompile(`(?i)(ver el|ver la|ver|muestra el|muestra la|muestra|muestrame|muéstrame|ensename|enseñame|ensena|dame|pasa|manda|mandame|enviame|envíame|mostrar)`)

// stopwordsTerm son palabras que se descartan al extraer el término de
// búsqueda de un mensaje de ficha/catálogo.
var stopwordsTerm = map[string]bool{
	"de": true, "del": true, "la": true, "el": true, "los": true, "las": true,
	"al": true, "un": true, "una": true, "unos": true, "unas": true, "para": true,
	"por": true, "me": true, "te": true, "se": true, "le": true, "que": true,
	"es": true, "en": true, "con": true, "y": true, "o": true, "a": true,
	"quiero": true, "mandame": true, "manda": true, "dame": true, "pasa": true,
	"envia": true, "enviame": true, "necesito": true, "ver": true, "mostrar": true,
	"muestra": true, "muestrame": true, "ensena": true, "ficha": true, "foto": true,
	"imagen": true, "card": true, "tarjeta": true, "vehiculo": true, "vehículo": true,
	"carro": true, "coche": true, "modelo": true, "camioneta": true, "catalogo": true,
	"catálogo": true, "porfa": true, "porfavor": true, "please": true, "gracias": true,
	"cuanto": true, "cuesta": true, "precio": true, "precios": true,
}

// reAyudaCotizar detecta preguntas sobre CÓMO se hace una cotización
// ("cómo hago una cotización?", "explica los pasos..."). Se responden de
// forma determinista para que el LLM no imite el wizard desde el historial.
var reAyudaCotizar = regexp.MustCompile(`(?i)(como|cómo|que es|qué es|que es un|qué es un|cual es|cuál es|como funciona|cómo funciona|explica|expliqueme|explícame|pasos|procedimiento|guia|guía|dime como|dime cómo)`)

// parseAyudaCotizar devuelve true si el mensaje pregunta cómo se cotiza.
func parseAyudaCotizar(text string) bool {
	low := norm(text)
	if !strings.Contains(low, "cotiza") {
		return false
	}
	return reAyudaCotizar.MatchString(low)
}

// classifyIntent decide la intención del mensaje sin LLM, en orden fijo:
// Cotizar > Ayuda > Ficha > Imprimir > Catálogo > Listar > Conversación.
func classifyIntent(text string) intent {
	low := norm(text)
	if parseIniciarCotizacion(text) {
		return intentCrearCotizacion
	}
	if _, ok := parseImprimirCotizacion(text); ok {
		return intentImprimirCotizacion
	}
	if parseFichaV2(low) {
		return intentFichaVehiculo
	}
	if parseCatalogo(low) {
		return intentCatalogo
	}
	if parseListarCotizaciones(text) {
		return intentListarCotizaciones
	}
	if parseAyudaCotizar(text) {
		return intentAyudaCotizar
	}
	return intentConversacion
}

// parseFichaV2 detecta peticiones de ficha/foto de vehículo. Los marcadores
// fuertes (ficha/card/tarjeta/foto/imagen) bastan; los débiles (muéstrame/ver/
// dame...) requieren contexto vehicular y no deben capturar "cotización".
func parseFichaV2(low string) bool {
	if reFichaFuerte.MatchString(low) {
		return true
	}
	if !reFichaDebil.MatchString(low) {
		return false
	}
	if strings.Contains(low, "cotizacion") {
		return false
	}
	return containsAny(low, []string{
		"vehiculo", "vehículo", "carro", "coche", "camioneta", "modelo", "marca",
		"dongfeng", "nissan", "paladin", "rich", "z9", "navara", "u-vane",
	})
}

// parseCatalogo detecta peticiones de catálogo/lista de vehículos/precios.
func parseCatalogo(low string) bool {
	if strings.Contains(low, "cotizacion") {
		return false
	}
	if containsAny(low, []string{
		"catalogo", "catálogo", "listado de vehiculos", "lista de vehiculos",
		"lista de carros", "listado de carros", "vehiculos disponibles",
		"carros disponibles", "que vehiculos hay", "que carros hay",
		"precios de los carros", "precios de los vehiculos", "precios del",
	}) {
		return true
	}
	vehCtx := containsAny(low, []string{
		"vehiculo", "vehículo", "carro", "coche", "camioneta", "modelo", "marca",
		"dongfeng", "nissan", "paladin", "rich", "z9", "navara", "u-vane",
	})
	if vehCtx && (strings.Contains(low, "cuesta") || strings.Contains(low, "cuanto vale") || strings.Contains(low, "precio")) {
		return true
	}
	if vehCtx && reListar.MatchString(low) {
		return true
	}
	return false
}

// handleRouterIntent ejecuta la intención determinista. Devuelve true si ya la
// manejó (no debe seguir al LLM).
func (b *Bot) handleRouterIntent(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string) bool {
	// Número suelto tras "Lista de cotizaciones": imprimir esa cotización
	// (el flujo cede ante la ambigüedad con last_list en process()).
	if b.handleListPick(ctx, chat, phone, emp, text) {
		return true
	}
	// "imprime la cotización" sin número: pedir el número (no dejar que el
	// LLM alucine que imprimió algo).
	if reImprimirIntencion.MatchString(norm(text)) {
		if _, ok := parseImprimirCotizacion(text); !ok {
			b.sendText(chat, "Indica el número de la cotización a imprimir. Ej: 'imprime la cotización 1'.")
			return true
		}
	}
	intentResult := classifyIntent(text)
	if intentResult == intentConversacion {
		flowName := b.preClasificarLLM(ctx, phone, emp, text)
		switch flowName {
		case "cotizacion":
			intentResult = intentCrearCotizacion
		case "listar_cotizaciones":
			intentResult = intentListarCotizaciones
		case "ficha_vehiculo":
			intentResult = intentFichaVehiculo
		case "catalogo_vehiculos":
			intentResult = intentCatalogo
		default:
			if flow := b.flowRegistry.FindByName(flowName); flow != nil {
				flow.Iniciar(phone, emp)
				return true
			}
		}
	}

	switch intentResult {
	case intentCrearCotizacion:
		b.flows.start(phone, emp)
		return true
	case intentFichaVehiculo:
		return b.handleFicha(ctx, chat, phone, emp, text)
	case intentImprimirCotizacion:
		if n, ok := parseImprimirCotizacion(text); ok {
			r := b.toolImprimirCotizacion(ctx, chat, phone, emp, fmt.Sprintf(`{"indice":%d}`, n))
			if directResultError(r) {
				b.sendText(chat, r)
			}
		}
		return true
	case intentCatalogo:
		b.sendCatalogo(ctx, chat, phone, emp, extractCatalogoTerm(text))
		return true
	case intentListarCotizaciones:
		b.flows.list(phone, emp)
		return true
	case intentAyudaCotizar:
		b.sendText(chat, mensajeComoCotizar)
		return true
	}
	return false
}

// handleFicha atiende la petición de la ficha/foto de un vehículo: por número
// de versión, por término (marca/modelo) o listando las coincidencias.
func (b *Bot) handleFicha(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string) bool {
	versions, err := cotizaciones.ObtenerVersiones(ctx, b.supa, emp.SocioComercial)
	if err != nil {
		b.sendText(chat, "No pude cargar el catálogo. Intenta de nuevo.")
		return true
	}
	low := norm(text)
	// Número de versión: "ficha del vehiculo 21", "card del modelo 30".
	if m := reFichaVehiculo.FindStringSubmatch(low); len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		for i := range versions {
			if versions[i].ID == int64(n) {
				r := b.sendFicha(ctx, chat, emp, versions[i], "", 0)
				if directResultError(r) {
					b.sendText(chat, r)
				}
				b.clearStateKey(ctx, phone, "ficha_pick")
				return true
			}
		}
		b.sendText(chat, fmt.Sprintf("No encontré la versión %d.", n))
		return true
	}
	term := extractTerm(text, fichaMarkers)
	if term == "" {
		b.sendText(chat, "¿De qué vehículo quieres la ficha? Ej: 'la ficha del Paladin' o 'ficha del vehiculo' + un número.")
		return true
	}
	// Término puramente numérico: tratar como ID de versión.
	if n, err := strconv.Atoi(term); err == nil {
		for i := range versions {
			if versions[i].ID == int64(n) {
				r := b.sendFicha(ctx, chat, emp, versions[i], "", 0)
				if directResultError(r) {
					b.sendText(chat, r)
				}
				b.clearStateKey(ctx, phone, "ficha_pick")
				return true
			}
		}
		b.sendText(chat, fmt.Sprintf("No encontré la versión %d.", n))
		return true
	}
	matches := matchVersions(versions, term)
	switch len(matches) {
	case 0:
		b.sendText(chat, "No encontré ningún vehículo con '"+term+"'. Puedes pedir 'el catálogo' o 'la ficha del vehiculo' + un número.")
	case 1:
		r := b.sendFicha(ctx, chat, emp, matches[0], "", 0)
		if directResultError(r) {
			b.sendText(chat, r)
		}
		b.clearStateKey(ctx, phone, "ficha_pick")
	default:
		var sb strings.Builder
		fmt.Fprintf(&sb, "Encontré varias versiones de %s:\n", term)
		for i, v := range matches {
			fmt.Fprintf(&sb, "%d. %s %s\n", i+1, v.MarcaNombre, displayName(v))
		}
		sb.WriteString("¿Cuál te envío?")
		b.sendText(chat, strings.TrimRight(sb.String(), "\n"))
		ids := make([]int64, len(matches))
		for i := range matches {
			ids[i] = matches[i].ID
		}
		b.setStateIDs(ctx, phone, "ficha_pick", ids)
	}
	return true
}

// handleFichaPick resuelve la selección pendiente de una ficha (el usuario
// eligió un número de la lista). Devuelve true si consumió el mensaje.
func (b *Bot) handleFichaPick(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string) bool {
	m := rePickNumber.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) < 2 {
		return false
	}
	ids := b.getStateIDs(ctx, phone, "ficha_pick")
	if len(ids) == 0 {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 || n > len(ids) {
		b.sendText(chat, fmt.Sprintf("Elige un número entre 1 y %d.", len(ids)))
		return true
	}
	versions, err := cotizaciones.ObtenerVersiones(ctx, b.supa, emp.SocioComercial)
	if err != nil {
		b.sendText(chat, "No pude cargar el catálogo. Intenta de nuevo.")
		return true
	}
	for i := range versions {
		if versions[i].ID == ids[n-1] {
			r := b.sendFicha(ctx, chat, emp, versions[i], "", 0)
			if directResultError(r) {
				b.sendText(chat, r)
			}
			b.clearStateKey(ctx, phone, "ficha_pick")
			return true
		}
	}
	b.sendText(chat, "No encontré ese vehículo.")
	b.clearStateKey(ctx, phone, "ficha_pick")
	return true
}

// handleListPick imprime la cotización N de la última lista de cotizaciones
// cuando el usuario responde con un número suelto ("1", "el 2", "la 3") tras
// "Lista de cotizaciones". Solo actúa si existe un listado reciente.
func (b *Bot) handleListPick(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string) bool {
	m := rePickNumber.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) < 2 {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	if _, ok := b.flows.pickCotizacion(phone, n); !ok {
		b.flows.restoreLastList(ctx, phone)
		if _, ok := b.flows.pickCotizacion(phone, n); !ok {
			return false
		}
	}
	r := b.toolImprimirCotizacion(ctx, chat, phone, emp, fmt.Sprintf(`{"indice":%d}`, n))
	if directResultError(r) {
		b.sendText(chat, r)
	}
	return true
}

// sendFicha renderiza y envía la card del vehículo como imagen PNG.
func (b *Bot) sendFicha(ctx context.Context, chat types.JID, emp *empleados.Empleado, v cotizaciones.Version, tipoPrecio string, custom float64) string {
	if custom > 0 && !b.isAdmin(ctx, emp) {
		custom = 0
	}
	png, perr := pdf.RenderVehicleCardPNG(v, tipoPrecio, custom)
	if perr != nil {
		return "No pude generar la ficha: " + perr.Error()
	}
	b.sendMediaQueued(chat, png, "image/png", fmt.Sprintf("ficha-%d.png", v.ID), true)
	return fmt.Sprintf("Ficha del vehículo %s %s enviada.", v.MarcaNombre, displayName(v))
}

// sendCatalogo envía el listado de vehículos (opcionalmente filtrado) y activa
// el CatalogoFlow para resolver la selección posterior ("8", "Id: 24").
func (b *Bot) sendCatalogo(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, term string) {
	out, ids := b.buildCatalogoList(ctx, emp, term)
	if strings.HasPrefix(out, "Error") {
		b.sendText(chat, out)
		return
	}
	if out == "" {
		b.sendText(chat, "No hay vehículos disponibles.")
		return
	}
	b.sendText(chat, out)
	if phone != "" && len(ids) > 0 {
		b.setStateIDs(ctx, phone, "catalogo_ids", ids)
		state, _ := b.state.Get(ctx, phone)
		state["catalogo_active"] = true
		state["catalogo_ts"] = time.Now().Format(time.RFC3339)
		_ = b.state.Set(ctx, phone, state)
	}
}

// buildCatalogoList construye el listado numerado de vehículos y devuelve el
// texto a enviar y los IDs en el MISMO orden de numeración (para que "8" = el
// octavo elemento de la lista, y "Id: 24" = la versión 24).
func (b *Bot) buildCatalogoList(ctx context.Context, emp *empleados.Empleado, term string) (string, []int64) {
	versions, err := cotizaciones.ObtenerVersiones(ctx, b.supa, emp.SocioComercial)
	if err != nil {
		return "Error listando catálogo: " + err.Error(), nil
	}
	var b2 strings.Builder
	ids := make([]int64, 0, len(versions))
	n := 0
	for _, v := range versions {
		if term != "" && !containsFold(v.MarcaNombre+v.ModeloNombre+v.NombreVersion, term) {
			continue
		}
		n++
		ids = append(ids, v.ID)
		nombre := displayName(v)
		if v.ModeloNombre == "" {
			nombre = v.NombreVersion
		}
		if n > 1 {
			b2.WriteString("\n")
		}
		fmt.Fprintf(&b2, "%d. *%s* (ID: %d)\n   - Precios: Estandar: %s / Premium: %s / Flota: %s USD",
			n, nombre, v.ID,
			formatQ(v.PrecioEstandar), formatQ(v.PrecioPremium), formatQ(v.PrecioFlota))
	}
	if term != "" {
		fmt.Fprintf(&b2, "\n(búsqueda: %s)", term)
	}
	return strings.TrimRight(b2.String(), "\n"), ids
}

// extractCatalogoTerm extrae el término de búsqueda de una petición de
// catálogo/precios ("precio del Paladin", "cuánto cuesta el RICH6 4x4").
func extractCatalogoTerm(text string) string {
	return extractTerm(text, []string{
		"que precio tiene", "cuanto cuesta", "cuanto vale", "precio del",
		"precio de la", "precio de", "precios", "catalogo", "catálogo",
		"lista de", "listado de", "vehiculos disponibles", "carros disponibles",
		"a cuanto", "a cuanto esta",
	})
}

// extractTerm devuelve las palabras significativas del texto tras el último
// marcador encontrado (ignora preposiciones y verbos comunes).
func extractTerm(text string, markers []string) string {
	low := norm(text)
	best := -1
	after := ""
	for _, mk := range markers {
		if i := strings.Index(low, mk); i >= 0 && i > best {
			best = i
			after = low[i+len(mk):]
		}
	}
	if best < 0 {
		return ""
	}
	// Cortar en la primera palabra de cierre (petición extra).
	for _, w := range []string{" porfa", " por favor", " gracias", " please", " quiero", " para ver"} {
		if i := strings.Index(after, w); i >= 0 {
			after = after[:i]
		}
	}
	words := strings.Fields(after)
	keep := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?¡¿()")
		if w == "" || stopwordsTerm[w] {
			continue
		}
		keep = append(keep, w)
	}
	return strings.Join(keep, " ")
}

// matchVersions devuelve las versiones cuyo marca/modelo/versión contiene
// todas las palabras significativas del término.
func matchVersions(versions []cotizaciones.Version, term string) []cotizaciones.Version {
	var out []cotizaciones.Version
	tokens := strings.Fields(norm(term))
	if len(tokens) == 0 {
		return out
	}
	for _, v := range versions {
		hay := norm(v.MarcaNombre + " " + v.ModeloNombre + " " + v.NombreVersion + " " + v.Clase + " " + v.Uso + " " + v.Tipo)
		all := true
		for _, t := range tokens {
			if !strings.Contains(hay, t) {
				all = false
				break
			}
		}
		if all {
			out = append(out, v)
		}
	}
	return out
}

// displayName arma el nombre legible de una versión evitando el duplicado
// (p. ej. Modelo "PALADIN" + Versión "PALADIN MID" -> "PALADIN MID").
func displayName(v cotizaciones.Version) string {
	modelo := strings.TrimSpace(v.ModeloNombre)
	version := strings.TrimSpace(v.NombreVersion)
	if version == "" {
		return modelo
	}
	if modelo == "" {
		return version
	}
	lv := norm(version)
	if lv == norm(modelo) || strings.HasPrefix(lv, norm(modelo)) {
		return version
	}
	return strings.TrimSpace(modelo + " " + version)
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// --- helpers de estado persistente (ficha_pick) ---

func (b *Bot) setStateIDs(ctx context.Context, phone, key string, ids []int64) {
	state, _ := b.state.Get(ctx, phone)
	data, _ := json.Marshal(ids)
	state[key] = string(data)
	_ = b.state.Set(ctx, phone, state)
}

func (b *Bot) getStateIDs(ctx context.Context, phone, key string) []int64 {
	state, _ := b.state.Get(ctx, phone)
	raw, ok := state[key].(string)
	if !ok || raw == "" {
		return nil
	}
	var ids []int64
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return nil
	}
	return ids
}

func (b *Bot) clearStateKey(ctx context.Context, phone, key string) {
	state, _ := b.state.Get(ctx, phone)
	if _, ok := state[key]; !ok {
		return
	}
	delete(state, key)
	_ = b.state.Set(ctx, phone, state)
}

package bot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"bot/internal/empleados"
)

// reImprimir busca el número N en frases "cotización N".
var reImprimirCotizacion = regexp.MustCompile(`(?i)cotizaci[oó]n[^0-9]{0,8}([0-9]+)`)

// reFichaVehiculo busca el número N en frases "ficha/card del vehículo N".
var reFichaVehiculo = regexp.MustCompile(`(?i)(?:ficha|card|tarjeta)[^0-9]{0,10}(?:vehiculo|vehículo|coche|carro|modelo)[^0-9]{0,10}([0-9]+)`)

// reVerboImprimir detecta la intención de imprimir/mostrar un documento.
var reVerboImprimir = regexp.MustCompile(`(?i)imprime|imprimir|imprimela|imprímela|pdf|manda|pasa|enviame|envíame|muestra|muéstrame|muestrame|ensena|enséñame|enseñame|ver`)

// reListar detecta la intención de ver el listado de cotizaciones.
var reListar = regexp.MustCompile(`(?i)listar|lista|listado|mostrar|mis|cuntas|cuantas|cada|todas|tengo|tiene|hay`)

// reListarSolo detecta la intención de listar sin mencionar "cotización"
// ("listar", "listame", "ver la lista", "listado"). El catálogo ("lista de
// vehiculos") lo captura parseCatalogo antes que este intent.
var reListarSolo = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:listar|listame|lista|listado)(?:$|[^a-z])`)

// reImprimirNumero acepta "imprime la 1", "imprímeme la 2" (sin la palabra
// "cotización"); el número se resuelve contra el último listado.
var reImprimirNumero = regexp.MustCompile(`(?i)imprim\w*[^0-9]{0,12}([0-9]{1,3})`)

// reImprimirIntencion detecta la petición de imprimir una cotización aunque
// no incluya el número ("imprime la cotización", "quiero imprimir la 3").
var reImprimirIntencion = regexp.MustCompile(`(?i)imprim\w*[^0-9]{0,14}cotizaci[oó]n|cotizaci[oó]n[^0-9]{0,14}imprim\w*`)

// reInterrogativoCrear descarta preguntas sobre CÓMO se cotiza (no son
// órdenes de crear: "cómo hago una cotización?" explica, no inicia wizard).
var reInterrogativoCrear = regexp.MustCompile(`(?i)^(como|cómo|que es|qué es|como se|como hago|cómo hago|como hacer|como haces|como funciona|cómo funciona|en que consiste|en qué consiste|explica|expliqueme|explícame|dime como|dime cómo|cual es el proceso|cuál es el proceso|cuales son los pasos|cuáles son los pasos|pasos para|procedimiento|que necesito|qué necesito)`)

// reCrearCotizacion detecta la intención de CREAR una cotización.
var reCrearCotizacion = regexp.MustCompile(`(?i)hazme|hacer|realiza|realizar|crea|crear|genera|generar|arma|necesito|quiero|cotizar|cotiza`)

// handleDirectIntent intercepta peticiones deterministas que no deben pasar por
// el LLM (imprimir, listar o crear cotizaciones), garantizando el mismo
// resultado con cualquier modelo. Devuelve true si ya la manejó.
func (b *Bot) handleDirectIntent(ctx context.Context, chat types.JID, phone string, emp *empleados.Empleado, text string) bool {
	if n, ok := parseImprimirCotizacion(text); ok {
		r := b.toolImprimirCotizacion(ctx, chat, phone, emp, fmt.Sprintf(`{"indice":%d}`, n))
		if directResultError(r) {
			b.sendText(chat, r)
		}
		return true
	}
	if n, ok := parseFichaVehiculo(text); ok {
		r := b.toolEnviarFicha(ctx, chat, phone, emp, fmt.Sprintf(`{"version_id":%d}`, n))
		if directResultError(r) {
			b.sendText(chat, r)
		}
		return true
	}
	if parseListarCotizaciones(text) {
		b.flows.list(phone, emp)
		return true
	}
	if parseIniciarCotizacion(text) {
		b.flows.start(phone, emp)
		return true
	}
	return false
}

// directResultError indica si el resultado de una tool de envío directo
// (ficha/imprimir) fue un error que el usuario debe ver: los mensajes de éxito
// dicen "enviada/enviado"; todo lo demás (Error/No pude/No encontré/Indica)
// es una falla que antes se tragaba en silencio.
func directResultError(r string) bool {
	return r != "" && !strings.Contains(r, "enviad")
}

// parseImprimirCotizacion extrae el número N de frases como
// "imprime la cotización 1", "imprimir cotización 3", "muéstrame la cotización 2".
func parseImprimirCotizacion(text string) (int, bool) {
	low := norm(text)
	// "imprime la cotización 1" (con la palabra cotización).
	if strings.Contains(low, "cotizacion") && reVerboImprimir.MatchString(low) {
		if m := reImprimirCotizacion.FindStringSubmatch(low); len(m) >= 2 {
			if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 {
				return n, true
			}
		}
	}
	// "imprime la 1", "imprímeme la 2": verbo imprimir + número suelto.
	if m := reImprimirNumero.FindStringSubmatch(low); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 {
			return n, true
		}
	}
	return 0, false
}

// parseFichaVehiculo extrae el ID de versión de frases como "ficha del
// vehículo 30", "card del vehiculo 3", "mándame la ficha del carro 12".
func parseFichaVehiculo(text string) (int, bool) {
	low := norm(text)
	if !strings.Contains(low, "ficha") && !strings.Contains(low, "card") && !strings.Contains(low, "tarjeta") {
		return 0, false
	}
	if !strings.Contains(low, "vehiculo") && !strings.Contains(low, "vehículo") &&
		!strings.Contains(low, "coche") && !strings.Contains(low, "carro") &&
		!strings.Contains(low, "modelo") {
		return 0, false
	}
	m := reFichaVehiculo.FindStringSubmatch(low)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// parseListarCotizaciones detecta peticiones como "ver mis cotizaciones",
// "lista de cotizaciones", "cuántas cotizaciones tengo".
func parseListarCotizaciones(text string) bool {
	low := norm(text)
	if strings.Contains(low, "cotizacion") {
		return reListar.MatchString(low)
	}
	// "listar", "listame", "lista", "listado" a secas: listado de cotizaciones
	// del mes (el catálogo ya fue capturado por parseCatalogo antes).
	return reListarSolo.MatchString(low)
}

// parseIniciarCotizacion detecta peticiones de CREAR una cotización como
// "hazme una cotización", "quiero cotizar", "cotiza el Z9". Se descarta si el
// mensaje pide listar/mostrar cotizaciones.
func parseIniciarCotizacion(text string) bool {
	low := norm(text)
	if reInterrogativoCrear.MatchString(low) {
		return false
	}
	if !reCrearCotizacion.MatchString(low) || !strings.Contains(low, "cotiza") {
		return false
	}
	if strings.Contains(low, "imprime") || strings.Contains(low, "imprimir") ||
		strings.Contains(low, "imprimeme") || strings.Contains(low, "reimprime") {
		return false
	}
	return !reListar.MatchString(low)
}

// norm normaliza texto: minúsculas y sin acentos/ñ.
func norm(s string) string {
	r := strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
		"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U", "Ü", "U", "Ñ", "N",
	)
	return strings.ToLower(r.Replace(s))
}

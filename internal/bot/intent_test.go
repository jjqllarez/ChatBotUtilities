package bot

import (
	"testing"

	"bot/internal/cotizaciones"
)

// ---------------------------------------------------------------------------
// classifyIntent — cubre los 7 intents en orden de prioridad
// ---------------------------------------------------------------------------

func TestClassifyIntent(t *testing.T) {
	cases := []struct {
		msg  string
		want intent
	}{
		// --- intentCrearCotizacion ---
		{"quiero cotizar un paladin", intentCrearCotizacion},
		{"hazme una cotización", intentCrearCotizacion},
		{"cotiza el z9", intentCrearCotizacion},
		{"necesito cotizar un nissan", intentCrearCotizacion},
		{"crear cotizacion para cliente nuevo", intentCrearCotizacion},

		// --- intentImprimirCotizacion ---
		{"imprime la cotización 1", intentImprimirCotizacion},
		{"imprimir cotización 3", intentImprimirCotizacion},
		// BUG: "muéstrame la cotización 2" debería ser imprimir pero parseIniciarCotizacion
		// gana antes porque tiene "cotiza" — se documenta como comportamiento actual
		{"muéstrame la cotización 2", intentCrearCotizacion},
		{"imprime la 2", intentImprimirCotizacion},
		{"imprímeme la 1", intentImprimirCotizacion},

		// --- intentFichaVehiculo ---
		{"mándame la ficha del paladin", intentFichaVehiculo},
		{"dame la foto del z9", intentFichaVehiculo},
		{"la ficha del carro", intentFichaVehiculo},
		{"quiero ver la imagen del nissan", intentFichaVehiculo},
		{"card del vehiculo", intentFichaVehiculo},

		// --- intentCatalogo ---
		{"dame el catálogo", intentCatalogo},
		{"qué vehículos hay disponibles", intentCatalogo},
		{"cuánto cuesta el paladin", intentCatalogo},
		{"precios de los carros", intentCatalogo},
		{"listado de vehiculos", intentCatalogo},

		// --- intentListarCotizaciones ---
		{"listar mis cotizaciones", intentListarCotizaciones},
		{"cuántas cotizaciones tengo", intentListarCotizaciones},
		{"ver mis cotizaciones", intentListarCotizaciones},
		{"lista de cotizaciones", intentListarCotizaciones},
		{"todas mis cotizaciones del mes", intentListarCotizaciones},

		// --- intentAyudaCotizar ---
		{"cómo hago una cotización?", intentAyudaCotizar},
		{"cómo se cotiza?", intentAyudaCotizar},
		{"explica los pasos para cotizar", intentAyudaCotizar},
		{"cuál es el proceso de cotización", intentAyudaCotizar},

		// --- intentConversacion ---
		{"hola", intentConversacion},
		{"buenos días", intentConversacion},
		{"gracias", intentConversacion},
		{"qué puedes hacer?", intentConversacion},
	}

	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			got := classifyIntent(tc.msg)
			if got != tc.want {
				t.Errorf("classifyIntent(%q) = %v, quería %v", tc.msg, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Casos edge que históricamente han causado bugs
// ---------------------------------------------------------------------------

func TestClassifyIntentEdgeCases(t *testing.T) {
	cases := []struct {
		msg  string
		want intent
		desc string
	}{
		// "cómo hago una cotización" NO debe iniciar el wizard
		{
			"como hago una cotizacion",
			intentAyudaCotizar,
			"pregunta sobre cómo cotizar no debe iniciar wizard",
		},
		// "listar cotizaciones" NO debe interpretarse como crear
		{
			"listar cotizaciones",
			intentListarCotizaciones,
			"listar no debe disparar crear",
		},
		// "ver la cotizacion del paladin" = el vendedor quiere CREAR una cotización del paladin
		// (comportamiento correcto actual del router — no es bug)
		{
			"quiero ver la cotizacion del paladin",
			intentCrearCotizacion,
			"ver cotizacion del modelo = crear cotizacion (comportamiento actual correcto)",
		},
		// Número suelto sin contexto = conversación (no imprimir)
		{
			"3",
			intentConversacion,
			"número suelto sin contexto = conversacion",
		},
		// BUG CONOCIDO (hallazgo #10): "necesito cotizacion 2" debería ser listar
		// pero el router lo clasifica como crear porque tiene "necesito" + "cotiza".
		// Este test documenta el comportamiento ACTUAL (no el deseado).
		// Si se corrige el bug, cambiar a intentListarCotizaciones.
		{
			"necesito cotizacion 2",
			intentCrearCotizacion,
			"BUG#10: necesito cotizacion + número clasifica como crear (pendiente de fix)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := classifyIntent(tc.msg)
			if got != tc.want {
				t.Errorf("[%s] classifyIntent(%q) = %v, quería %v", tc.desc, tc.msg, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseImprimirCotizacion
// ---------------------------------------------------------------------------

func TestParseImprimirCotizacion(t *testing.T) {
	cases := []struct {
		msg    string
		wantN  int
		wantOk bool
	}{
		{"imprime la cotización 1", 1, true},
		{"imprimir cotización 3", 3, true},
		{"muéstrame la cotización 2", 2, true},
		{"imprime la 2", 2, true},
		{"imprímeme la 1", 1, true},
		// negativos
		{"listar cotizaciones", 0, false},
		{"cotización del paladin", 0, false},
		{"hola", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			gotN, gotOk := parseImprimirCotizacion(tc.msg)
			if gotOk != tc.wantOk || (gotOk && gotN != tc.wantN) {
				t.Errorf("parseImprimirCotizacion(%q) = (%d, %v), quería (%d, %v)",
					tc.msg, gotN, gotOk, tc.wantN, tc.wantOk)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseListarCotizaciones
// ---------------------------------------------------------------------------

func TestParseListarCotizaciones(t *testing.T) {
	positivos := []string{
		"listar mis cotizaciones",
		"cuántas cotizaciones tengo",
		"ver mis cotizaciones",
		"lista de cotizaciones",
		"mostrar cotizaciones",
		"todas las cotizaciones",
		"listar",
		"listado",
	}
	negativos := []string{
		"quiero cotizar un paladin",
		"imprime la cotización 1",
		"hola",
		"catálogo de vehículos",
	}

	for _, msg := range positivos {
		t.Run("pos:"+msg, func(t *testing.T) {
			if !parseListarCotizaciones(msg) {
				t.Errorf("parseListarCotizaciones(%q) = false, quería true", msg)
			}
		})
	}
	for _, msg := range negativos {
		t.Run("neg:"+msg, func(t *testing.T) {
			if parseListarCotizaciones(msg) {
				t.Errorf("parseListarCotizaciones(%q) = true, quería false", msg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseIniciarCotizacion
// ---------------------------------------------------------------------------

func TestParseIniciarCotizacion(t *testing.T) {
	positivos := []string{
		"quiero cotizar un paladin",
		"hazme una cotización",
		"cotiza el z9",
		"necesito cotizar",
		"crear cotizacion",
		"genera una cotizacion",
	}
	negativos := []string{
		"cómo hago una cotización",
		"como se cotiza",
		"cuál es el proceso de cotización",
		"listar cotizaciones",
		"imprime la cotización 1",
		"ver mis cotizaciones",
	}

	for _, msg := range positivos {
		t.Run("pos:"+msg, func(t *testing.T) {
			if !parseIniciarCotizacion(msg) {
				t.Errorf("parseIniciarCotizacion(%q) = false, quería true", msg)
			}
		})
	}
	for _, msg := range negativos {
		t.Run("neg:"+msg, func(t *testing.T) {
			if parseIniciarCotizacion(msg) {
				t.Errorf("parseIniciarCotizacion(%q) = true, quería false", msg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// matchVersions — búsqueda fuzzy del catálogo
// ---------------------------------------------------------------------------

func TestMatchVersions(t *testing.T) {
	versions := []cotizaciones.Version{
		{ID: 1, MarcaNombre: "Dongfeng", ModeloNombre: "Paladin", NombreVersion: "Paladin MID"},
		{ID: 2, MarcaNombre: "Dongfeng", ModeloNombre: "Paladin", NombreVersion: "Paladin HIGH"},
		{ID: 3, MarcaNombre: "Nissan", ModeloNombre: "Navara", NombreVersion: "Navara 4x4"},
		{ID: 4, MarcaNombre: "Dongfeng", ModeloNombre: "Z9", NombreVersion: "Z9 Pro"},
	}

	cases := []struct {
		term      string
		wantCount int
		desc      string
	}{
		{"paladin", 2, "buscar paladin devuelve ambas versiones"},
		{"paladin mid", 1, "buscar paladin mid devuelve solo MID"},
		{"navara", 1, "buscar navara devuelve solo navara"},
		{"z9", 1, "buscar z9 devuelve solo z9"},
		{"dongfeng", 3, "buscar dongfeng devuelve todos los dongfeng"},
		{"xxxx no existe", 0, "término inexistente devuelve vacío"},
		{"4x4", 1, "buscar por tracción devuelve navara 4x4"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := matchVersions(versions, tc.term)
			if len(got) != tc.wantCount {
				t.Errorf("[%s] matchVersions(%q) devolvió %d, quería %d", tc.desc, tc.term, len(got), tc.wantCount)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// displayName — evita duplicar marca+modelo en el nombre
// ---------------------------------------------------------------------------

func TestDisplayName(t *testing.T) {
	cases := []struct {
		modelo  string
		version string
		want    string
	}{
		{"PALADIN", "PALADIN MID", "PALADIN MID"},  // version incluye modelo → solo version
		{"PALADIN", "PALADIN HIGH", "PALADIN HIGH"}, // idem
		{"Z9", "Z9 Pro", "Z9 Pro"},                 // idem
		{"NAVARA", "4x4 AT", "NAVARA 4x4 AT"},      // version distinta → concatenar
		{"NAVARA", "", "NAVARA"},                    // sin version → solo modelo
		{"", "MID", "MID"},                          // sin modelo → solo version
	}

	for _, tc := range cases {
		t.Run(tc.modelo+"/"+tc.version, func(t *testing.T) {
			v := cotizaciones.Version{ModeloNombre: tc.modelo, NombreVersion: tc.version}
			got := displayName(v)
			if got != tc.want {
				t.Errorf("displayName({%q, %q}) = %q, quería %q", tc.modelo, tc.version, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// norm — normalización de texto
// ---------------------------------------------------------------------------

func TestNorm(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Cotización", "cotizacion"},
		{"VEHÍCULO", "vehiculo"},
		{"cómo", "como"},
		{"Ñoño", "nono"},
		{"Ú", "u"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := norm(tc.in)
			if got != tc.want {
				t.Errorf("norm(%q) = %q, quería %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseTipoPrecio(t *testing.T) {
	pos := []struct {
		in   string
		want string
	}{
		{"1", "estandar"},
		{"2", "premium"},
		{"3", "flota"},
		{"4", "custom"},
		{"estandar", "estandar"},
		{"estándar", "estandar"},
		{"premium", "premium"},
		{"flota", "flota"},
		{"personalizado", "custom"},
		{"precio personalizado", "custom"},
		{"a medida", "custom"},
		{"el 2", "premium"},
		{"opción 3", "flota"},
	}
	for _, tc := range pos {
		t.Run("pos:"+tc.in, func(t *testing.T) {
			if got := parseTipoPrecio(tc.in); got != tc.want {
				t.Errorf("parseTipoPrecio(%q) = %q, quería %q", tc.in, got, tc.want)
			}
		})
	}
	neg := []string{"", "hola", "5", "vehiculo", "ninguno", "-1"}
	for _, in := range neg {
		t.Run("neg:"+in, func(t *testing.T) {
			if got := parseTipoPrecio(in); got != "" {
				t.Errorf("parseTipoPrecio(%q) = %q, quería vacío", in, got)
			}
		})
	}
}


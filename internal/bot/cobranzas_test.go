package bot

import (
	"errors"
	"io"
	"log"
	"testing"
)

// TestEstadoTrasEnvio valida la política de reintentos de envío:
// 1 intento inicial + máximo maxEnvioReintentos (2) reintentos; luego 'error'
// definitivo. Los reintentos dejan la fila en 'pendiente' con `intentos`
// incrementado, por lo que se vuelven a tomar en el siguiente ciclo del poll.
func TestEstadoTrasEnvio(t *testing.T) {
	b := &Bot{log: log.New(io.Discard, "", 0)}
	errEnvio := errors.New("fallo de envío")

	cases := []struct {
		name     string
		sendErr  error
		intentos int64
		want     string
	}{
		{"éxito en el intento inicial", nil, 0, "enviado"},
		{"éxito tras reintentos", nil, 2, "enviado"},
		{"fallo del intento inicial -> reintentar", errEnvio, 0, "pendiente"},
		{"fallo del primer reintento -> reintentar", errEnvio, 1, "pendiente"},
		{"fallo del segundo reintento -> error", errEnvio, 2, "error"},
		{"fallo con intentos ya agotados -> error", errEnvio, 7, "error"},
	}
	for _, c := range cases {
		got, _ := b.estadoTrasEnvio("test", 1, c.sendErr, c.intentos)
		if got != c.want {
			t.Errorf("%s: estadoTrasEnvio(intentos=%d) = %q; want %q",
				c.name, c.intentos, got, c.want)
		}
	}
}

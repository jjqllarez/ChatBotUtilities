package correo

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func clave(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func TestCifrarDescifrar(t *testing.T) {
	key := clave(t)
	const plain = "P@ssw0rd-ñ-123"

	enc, err := Cifrar(plain, key)
	if err != nil {
		t.Fatalf("Cifrar: %v", err)
	}
	if enc == plain {
		t.Fatal("el ciphertext no debe ser igual al texto plano")
	}
	got, err := Descifrar(enc, key)
	if err != nil {
		t.Fatalf("Descifrar: %v", err)
	}
	if got != plain {
		t.Fatalf("round-trip = %q; want %q", got, plain)
	}

	// Cifrar dos veces el mismo texto debe dar distinto (nonce aleatorio).
	enc2, _ := Cifrar(plain, key)
	if enc == enc2 {
		t.Fatal("dos cifrados del mismo texto no deberían coincidir (nonce)")
	}
}

func TestDescifrarLlaveIncorrecta(t *testing.T) {
	enc, err := Cifrar("secreto", clave(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Descifrar(enc, clave(t)); err == nil {
		t.Fatal("descifrar con otra llave debería fallar")
	}
}

func TestLlaveInvalida(t *testing.T) {
	if _, err := Cifrar("x", "no-es-base64"); err == nil {
		t.Fatal("base64 inválido debería fallar")
	}
	corta := base64.StdEncoding.EncodeToString([]byte("corta"))
	if _, err := Cifrar("x", corta); err == nil {
		t.Fatal("llave de longitud incorrecta debería fallar")
	}
}

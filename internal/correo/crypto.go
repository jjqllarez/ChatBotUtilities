// Package correo contiene el cifrado de credenciales (AES-256-GCM) y el envío
// por SMTP para la cola de correos.
//
// La contraseña del remitente se guarda cifrada en Supabase
// (`cuentas_correo.password_enc`); la llave (32 bytes en base64) vive SOLO en
// el .env del bot (EMAIL_CRED_KEY). El bot es el único que cifra/descifra.
package correo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// aad fija el contexto del cifrado: un ciphertext de esta columna no es válido
// en otro contexto. Cambiar este valor invalida las credenciales existentes.
const aad = "cuentas_correo:v1"

// Cifrar cifra `plain` con AES-256-GCM. `keyB64` es la llave (32 bytes en
// base64). Devuelve base64(nonce||ciphertext).
func Cifrar(plain, keyB64 string) (string, error) {
	key, err := decodeKey(keyB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), []byte(aad))
	return base64.StdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// Descifrar invierte Cifrar.
func Descifrar(enc, keyB64 string) (string, error) {
	key, err := decodeKey(keyB64)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		return "", fmt.Errorf("credencial no es base64 válido: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("credencial cifrada demasiado corta")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := gcm.Open(nil, nonce, ct, []byte(aad))
	if err != nil {
		return "", fmt.Errorf("no se pudo descifrar (¿EMAIL_CRED_KEY incorrecta?): %w", err)
	}
	return string(plain), nil
}

func decodeKey(keyB64 string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil {
		return nil, fmt.Errorf("EMAIL_CRED_KEY no es base64 válido: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("EMAIL_CRED_KEY debe tener 32 bytes (tiene %d)", len(k))
	}
	return k, nil
}

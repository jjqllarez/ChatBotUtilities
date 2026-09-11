// Comando administrativo para registrar/actualizar una cuenta remitente de
// correo con la contraseña CIFRADA (AES-256-GCM).
//
// Uso (desde el directorio con el .env, p. ej. /opt/whatsbot):
//
//	echo 'LA_CONTRASEÑA' | ./setmail -email servidor@dongfengve.com -nombre "Capital Motors C.A." -socio 1
//
// La contraseña se lee de stdin (una línea) para no dejarla en el historial ni
// en la lista de procesos. Requiere EMAIL_CRED_KEY en el entorno/.env.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"bot/internal/config"
	"bot/internal/correo"
	"bot/internal/supabase"
)

func main() {
	email := flag.String("email", "", "correo remitente (From)")
	nombre := flag.String("nombre", "", "nombre visible del remitente")
	socio := flag.Int("socio", 1, "socio_comercial")
	host := flag.String("host", "smtp.purelymail.com", "SMTP host")
	port := flag.Int("port", 465, "SMTP port")
	seguridad := flag.String("seguridad", "ssl", "ssl|starttls|ninguna")
	usuario := flag.String("usuario", "", "usuario SMTP (default = email)")
	flag.Parse()

	if strings.TrimSpace(*email) == "" || strings.TrimSpace(*nombre) == "" {
		log.Fatal("uso: setmail -email X -nombre Y [-socio 1] [-host smtp.purelymail.com] [-port 465] [-seguridad ssl] [-usuario X]")
	}
	if *usuario == "" {
		*usuario = *email
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	key := strings.TrimSpace(os.Getenv("EMAIL_CRED_KEY"))
	if key == "" {
		log.Fatal("falta EMAIL_CRED_KEY en el entorno/.env")
	}

	fmt.Fprintf(os.Stderr, "Contraseña SMTP para %s (stdin): ", *email)
	sc := bufio.NewScanner(os.Stdin)
	sc.Scan()
	pass := strings.TrimSpace(sc.Text())
	if pass == "" {
		log.Fatal("contraseña vacía")
	}

	enc, err := correo.Cifrar(pass, key)
	if err != nil {
		log.Fatalf("cifrando: %v", err)
	}

	cli := supabase.NewClient(cfg.SupabaseURL, cfg.SupabaseServiceKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	row := map[string]any{
		"socio_comercial":  *socio,
		"nombre_remitente": *nombre,
		"email":            *email,
		"smtp_host":        *host,
		"smtp_port":        *port,
		"smtp_seguridad":   *seguridad,
		"smtp_usuario":     *usuario,
		"password_enc":     enc,
		"activo":           true,
		"updated_at":       time.Now().UTC().Format(time.RFC3339),
	}
	if err := cli.Upsert(ctx, "cuentas_correo", row, []string{"socio_comercial", "email"}); err != nil {
		log.Fatalf("guardando cuenta: %v", err)
	}
	fmt.Printf("OK: cuenta %s registrada/actualizada (socio %d).\n", *email, *socio)
}

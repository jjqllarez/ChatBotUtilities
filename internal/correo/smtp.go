package correo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wneessen/go-mail"
)

// Cuenta son los datos de envío de un remitente (ya descifrados).
type Cuenta struct {
	Nombre    string
	Email     string
	Usuario   string
	Password  string
	Host      string
	Port      int
	Seguridad string // ssl | starttls | ninguna
}

// Resultado describe el desenlace de un envío.
type Resultado struct {
	Codigo   int  // código SMTP (si aplica)
	Temporal bool // true = reintentable (4xx / red); false = permanente (5xx)
	Err      error
}

// Enviar manda un correo con versión texto y HTML. Si no se da texto, se
// deriva del HTML.
func (c Cuenta) Enviar(ctx context.Context, para, asunto, cuerpoHTML, cuerpoTexto string) Resultado {
	msg := mail.NewMsg()
	if err := msg.FromFormat(c.Nombre, c.Email); err != nil {
		return Resultado{Err: fmt.Errorf("remitente inválido: %w", err)}
	}
	if err := msg.To(strings.TrimSpace(para)); err != nil {
		return Resultado{Err: fmt.Errorf("destinatario inválido: %w", err)}
	}
	msg.Subject(asunto)

	if strings.TrimSpace(cuerpoTexto) == "" {
		cuerpoTexto = textoDesdeHTML(cuerpoHTML)
	}
	if cuerpoTexto != "" {
		msg.SetBodyString(mail.TypeTextPlain, cuerpoTexto)
		if strings.TrimSpace(cuerpoHTML) != "" {
			msg.AddAlternativeString(mail.TypeTextHTML, cuerpoHTML)
		}
	} else {
		msg.SetBodyString(mail.TypeTextHTML, cuerpoHTML)
	}

	opts := []mail.Option{
		mail.WithPort(c.Port),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(c.Usuario),
		mail.WithPassword(c.Password),
		mail.WithTimeout(60 * time.Second),
	}
	switch strings.ToLower(strings.TrimSpace(c.Seguridad)) {
	case "starttls":
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	case "ninguna":
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	default: // ssl (implícito, puerto 465)
		opts = append(opts, mail.WithSSL())
	}

	cli, err := mail.NewClient(c.Host, opts...)
	if err != nil {
		return Resultado{Err: err}
	}
	if err := cli.DialAndSendWithContext(ctx, msg); err != nil {
		var se *mail.SendError
		if errors.As(err, &se) {
			return Resultado{Codigo: se.ErrorCode(), Temporal: se.IsTemp(), Err: err}
		}
		// Errores de red/conexión no tipados: tratarlos como temporales.
		return Resultado{Temporal: true, Err: err}
	}
	return Resultado{Codigo: 250}
}

var reTagHTML = regexp.MustCompile(`(?s)<[^>]*>`)

// textoDesdeHTML deriva una versión texto simple cuando el productor no la dio.
func textoDesdeHTML(s string) string {
	if s == "" {
		return ""
	}
	s = reTagHTML.ReplaceAllString(s, " ")
	repl := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&aacute;", "á", "&eacute;", "é",
		"&iacute;", "í", "&oacute;", "ó", "&uacute;", "ú", "&ntilde;", "ñ",
	)
	return strings.Join(strings.Fields(repl.Replace(s)), " ")
}

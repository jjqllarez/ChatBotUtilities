package empleados

import (
	"context"
	"strings"
	"unicode"

	"bot/internal/supabase"
)

type Empleado struct {
	ID             int64
	UserID         string `json:"user_id"`
	SocioComercial int64  `json:"socio_comercial_id"`
	Identificacion string
	NombreCompleto string
	Telefono       string
	Activo         bool
}

// NormalizePhone keeps only digits for a robust comparison.
func NormalizePhone(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LookupByPhone finds an active employee whose telefono matches the given phone.
// The phone (as sent by whatsapp, without +) is matched in digit form.
func LookupByPhone(ctx context.Context, client *supabase.Client, phone string) (*Empleado, error) {
	target := NormalizePhone(phone)

	rows, err := client.Select(ctx, "empleados", "?select=id,user_id,socio_comercial_id,identificacion,nombre_completo,telefono,activo&activo=eq.true")
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		tel := supabase.GetString(r, "telefono")
		if NormalizePhone(tel) == target {
			return &Empleado{
				ID:             supabase.GetInt(r, "id"),
				UserID:         supabase.GetString(r, "user_id"),
				SocioComercial: supabase.GetInt(r, "socio_comercial_id"),
				Identificacion: supabase.GetString(r, "identificacion"),
				NombreCompleto: supabase.GetString(r, "nombre_completo"),
				Telefono:       tel,
				Activo:         supabase.GetBool(r, "activo"),
			}, nil
		}
	}
	return nil, nil
}

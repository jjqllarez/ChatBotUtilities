package bot

import (
	"context"
	"fmt"
	"strings"

	"bot/internal/empleados"
	"bot/internal/llm"
)

// preClasificarLLM usa el LLM para clasificar un mensaje ambiguo entre los
// flujos registrados en la tabla bot_flows de Supabase.
// Se invoca únicamente cuando el router determinista no pudo clasificar el mensaje.
func (b *Bot) preClasificarLLM(ctx context.Context, phone string, emp *empleados.Empleado, text string) string {
	if b.llmClient == nil {
		return "conversacion"
	}

	metas := b.flowRegistry.LoadMeta(ctx, int(emp.SocioComercial))
	if len(metas) == 0 {
		return "conversacion"
	}

	var sb strings.Builder
	sb.WriteString("Eres un clasificador de intención de mensajes de WhatsApp para vendedores automotrices.\n")
	sb.WriteString("Clasifica el siguiente mensaje en EXACTAMENTE UNA de estas categorías (responde SOLO el nombre en minúsculas):\n\n")

	for _, m := range metas {
		fmt.Fprintf(&sb, "- %s: %s\n", m.Nombre, m.Descripcion)
		if len(m.FrasesEjemplo) > 0 {
			ejemplos := m.FrasesEjemplo
			if len(ejemplos) > 3 {
				ejemplos = ejemplos[:3]
			}
			fmt.Fprintf(&sb, "  Ejemplos: \"%s\"\n", strings.Join(ejemplos, "\", \""))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("- conversacion: cualquier otra frase, saludo, o pregunta libre\n\n")

	hist, err := b.history.Recent(ctx, phone, 3)
	if err == nil && len(hist) > 0 {
		sb.WriteString("Contexto reciente:\n")
		for _, h := range hist {
			fmt.Fprintf(&sb, "%s: %s\n", h.Role, h.Content)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "Mensaje actual: \"%s\"\n\nCategoría:", text)

	respMsg, err := b.llmClient.Chat(ctx, []llm.Message{
		{Role: "user", Content: sb.String()},
	}, nil)
	if err != nil {
		return "conversacion"
	}

	cat := strings.TrimSpace(strings.ToLower(respMsg.Content))
	for _, m := range metas {
		if strings.EqualFold(cat, m.Nombre) {
			return m.Nombre
		}
	}

	return "conversacion"
}

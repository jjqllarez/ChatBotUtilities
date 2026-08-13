package bot

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"bot/internal/supabase"
)

// HistoryMsg es un mensaje del historial persistente.
type HistoryMsg struct {
	Role      string
	Content   string
	CreatedAt string
}

// HistoryStore guarda el historial de conversación por teléfono en
// bot_chat_history.
type HistoryStore struct {
	c *supabase.Client
}

func NewHistoryStore(c *supabase.Client) *HistoryStore {
	return &HistoryStore{c: c}
}

// Append añade un mensaje al historial.
func (h *HistoryStore) Append(ctx context.Context, phone, role, content string) error {
	if content == "" {
		return nil
	}
	return h.c.Insert(ctx, "bot_chat_history", map[string]any{
		"phone": phone, "role": role, "content": content,
	})
}

// Recent devuelve los últimos N mensajes en orden cronológico.
func (h *HistoryStore) Recent(ctx context.Context, phone string, limit int) ([]HistoryMsg, error) {
	rows, err := h.c.Select(ctx, "bot_chat_history",
		fmt.Sprintf("?select=role,content,created_at&phone=eq.%s&order=created_at.desc&limit=%d", url.QueryEscape(phone), limit))
	if err != nil {
		return nil, err
	}
	out := make([]HistoryMsg, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		out = append(out, HistoryMsg{
			Role:      supabase.GetString(r, "role"),
			Content:   supabase.GetString(r, "content"),
			CreatedAt: supabase.GetString(r, "created_at"),
		})
	}
	return out, nil
}

// Clean borra mensajes más antiguos que retainHours.
func (h *HistoryStore) Clean(ctx context.Context, retainHours int) error {
	cutoff := time.Now().Add(-time.Duration(retainHours) * time.Hour).Format(time.RFC3339)
	return h.c.Delete(ctx, "bot_chat_history",
		"?created_at=lt."+url.QueryEscape(cutoff))
}
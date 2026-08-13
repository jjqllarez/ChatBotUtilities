package bot

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"bot/internal/supabase"
)

// StateStore persiste el estado de la conversación (draft de cotización,
// contexto) por teléfono en bot_chat_state.
type StateStore struct {
	c *supabase.Client
}

func NewStateStore(c *supabase.Client) *StateStore {
	return &StateStore{c: c}
}

// Get devuelve el estado JSON de un teléfono (mapa vacío si no existe).
func (s *StateStore) Get(ctx context.Context, phone string) (map[string]any, error) {
	row, ok, err := s.c.SelectOne(ctx, "bot_chat_state",
		"?select=state&phone=eq."+url.QueryEscape(phone))
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if !ok {
		return out, nil
	}
	state := row["state"]
	if state == nil {
		return out, nil
	}
	switch v := state.(type) {
	case map[string]any:
		return v, nil
	case string:
		if v != "" {
			_ = json.Unmarshal([]byte(v), &out)
		}
	}
	return out, nil
}

// Set guarda (upsert) el estado JSON de un teléfono.
func (s *StateStore) Set(ctx context.Context, phone string, state map[string]any) error {
	if state == nil {
		state = map[string]any{}
	}
	return s.c.Upsert(ctx, "bot_chat_state", map[string]any{
		"phone": phone,
		"state": state,
		"updated_at": nowRFC3339(),
	}, []string{"phone"})
}

// Delete elimina el estado de un teléfono.
func (s *StateStore) Delete(ctx context.Context, phone string) error {
	return s.c.Delete(ctx, "bot_chat_state", "?phone=eq."+url.QueryEscape(phone))
}

func nowRFC3339() string {
	return time.Now().Format("2006-01-02T15:04:05.000Z07:00")
}
package supabase

import (
	"context"
	"net/url"
	"strings"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// LIDMap implements the global LID<->PN store backed by whatsmeow_lid_map.
type LIDMap struct {
	*Container
}

var _ store.LIDStore = (*LIDMap)(nil)

func NewLIDMap(c *Container) *LIDMap {
	return &LIDMap{Container: c}
}

func (l *LIDMap) PutLIDMapping(ctx context.Context, lid, jid types.JID) error {
	return l.PutManyLIDMappings(ctx, []store.LIDMapping{{LID: lid, PN: jid}})
}

func (l *LIDMap) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	for _, m := range mappings {
		if err := l.client.Upsert(ctx, "whatsmeow_lid_map", map[string]any{
			"lid": m.LID.User, "pn": m.PN.User,
		}, []string{"lid"}); err != nil {
			return err
		}
	}
	return nil
}

func (l *LIDMap) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	row, ok, err := l.client.SelectOne(ctx, "whatsmeow_lid_map", "?select=pn&"+kv("lid", lid.User))
	if err != nil || !ok {
		return types.EmptyJID, err
	}
	pn := types.NewJID(GetString(row, "pn"), types.DefaultUserServer)
	return pn, nil
}

func (l *LIDMap) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	row, ok, err := l.client.SelectOne(ctx, "whatsmeow_lid_map", "?select=lid&"+kv("pn", pn.User))
	if err != nil || !ok {
		return types.EmptyJID, err
	}
	lid := types.NewJID(GetString(row, "lid"), types.HiddenUserServer)
	return lid, nil
}

func (l *LIDMap) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	out := make(map[types.JID]types.JID, len(pns))
	if len(pns) == 0 {
		return out, nil
	}
	parts := make([]string, 0, len(pns))
	for _, pn := range pns {
		parts = append(parts, "pn.eq."+url.QueryEscape(pn.User))
	}
	q := "?select=lid,pn&or=(" + strings.Join(parts, ",") + ")"
	rows, err := l.client.Select(ctx, "whatsmeow_lid_map", q)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		pn := types.NewJID(GetString(r, "pn"), types.DefaultUserServer)
		lid := types.NewJID(GetString(r, "lid"), types.HiddenUserServer)
		out[pn] = lid
	}
	return out, nil
}

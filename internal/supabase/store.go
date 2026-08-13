package supabase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
)

// Store implements all the per-device whatsmeow stores backed by Supabase REST.
type Store struct {
	*Container
	JID string

	preKeyLock sync.Mutex

	contactCache     map[types.JID]*types.ContactInfo
	contactCacheLock sync.Mutex

	migratedPNSessionsCache map[string]bool
}

var _ store.AllSessionSpecificStores = (*Store)(nil)

func NewStore(c *Container, jid types.JID) *Store {
	return &Store{
		Container:               c,
		JID:                     jid.String(),
		contactCache:            make(map[types.JID]*types.ContactInfo),
		migratedPNSessionsCache: make(map[string]bool),
	}
}

func (s *Store) jidFilter() string { return kv("our_jid", s.JID) }

// --- IdentityStore ---

func (s *Store) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	return s.client.Upsert(ctx, "whatsmeow_identity_keys", map[string]any{
		"our_jid": s.JID, "their_id": address, "identity": key[:],
	}, []string{"our_jid", "their_id"})
}

func (s *Store) DeleteAllIdentities(ctx context.Context, phone string) error {
	q := "?" + s.jidFilter() + "&their_id=like." + url.QueryEscape(phone+":%")
	return s.client.Delete(ctx, "whatsmeow_identity_keys", q)
}

func (s *Store) DeleteIdentity(ctx context.Context, address string) error {
	q := "?" + s.jidFilter() + "&" + kv("their_id", address)
	return s.client.Delete(ctx, "whatsmeow_identity_keys", q)
}

func (s *Store) IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error) {
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_identity_keys", "?select=identity&"+s.jidFilter()+"&"+kv("their_id", address))
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	existing := GetBytes(row, "identity")
	if len(existing) != 32 {
		return false, errors.New("supabase: illegal identity length")
	}
	return *(*[32]byte)(existing) == key, nil
}

// --- SessionStore ---

func (s *Store) GetSession(ctx context.Context, address string) ([]byte, error) {
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_sessions", "?select=session&"+s.jidFilter()+"&"+kv("their_id", address))
	if err != nil || !ok {
		return nil, err
	}
	return GetBytes(row, "session"), nil
}

func (s *Store) HasSession(ctx context.Context, address string) (bool, error) {
	_, ok, err := s.client.SelectOne(ctx, "whatsmeow_sessions", "?select=session&"+s.jidFilter()+"&"+kv("their_id", address))
	return ok, err
}

func (s *Store) GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(addresses))
	if len(addresses) == 0 {
		return result, nil
	}
	parts := make([]string, 0, len(addresses))
	for _, a := range addresses {
		parts = append(parts, "their_id.eq."+url.QueryEscape(a))
		result[a] = nil
	}
	q := "?select=their_id,session&" + s.jidFilter() + "&or=(" + strings.Join(parts, ",") + ")"
	rows, err := s.client.Select(ctx, "whatsmeow_sessions", q)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[GetString(r, "their_id")] = GetBytes(r, "session")
	}
	return result, nil
}

func (s *Store) PutSession(ctx context.Context, address string, session []byte) error {
	return s.client.Upsert(ctx, "whatsmeow_sessions", map[string]any{
		"our_jid": s.JID, "their_id": address, "session": session,
	}, []string{"our_jid", "their_id"})
}

func (s *Store) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	for addr, sess := range sessions {
		if err := s.PutSession(ctx, addr, sess); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteAllSessions(ctx context.Context, phone string) error {
	q := "?" + s.jidFilter() + "&their_id=like." + url.QueryEscape(phone+":%")
	return s.client.Delete(ctx, "whatsmeow_sessions", q)
}

func (s *Store) DeleteSession(ctx context.Context, address string) error {
	q := "?" + s.jidFilter() + "&" + kv("their_id", address)
	return s.client.Delete(ctx, "whatsmeow_sessions", q)
}

func (s *Store) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	key := pn.SignalAddressUser()
	if s.migratedPNSessionsCache[key] {
		return nil
	}
	s.migratedPNSessionsCache[key] = true
	return nil
}

// --- PreKeyStore ---

func (s *Store) nextPreKeyID(ctx context.Context) (uint32, error) {
	rows, err := s.client.Select(ctx, "whatsmeow_pre_keys", "?select=key_id&"+kv("jid", s.JID)+"&order=key_id.desc&limit=1")
	if err != nil {
		return 0, err
	}
	var maxID int64 = 0
	if len(rows) > 0 {
		maxID = GetInt(rows[0], "key_id")
	}
	return uint32(maxID) + 1, nil
}

func (s *Store) genOnePreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	key := keys.NewPreKey(id)
	err := s.client.Insert(ctx, "whatsmeow_pre_keys", map[string]any{
		"jid": s.JID, "key_id": key.KeyID, "key": key.Priv[:], "uploaded": true,
	})
	return key, err
}

func (s *Store) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	s.preKeyLock.Lock()
	defer s.preKeyLock.Unlock()
	nextID, err := s.nextPreKeyID(ctx)
	if err != nil {
		return nil, err
	}
	return s.genOnePreKey(ctx, nextID)
}

func (s *Store) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	s.preKeyLock.Lock()
	defer s.preKeyLock.Unlock()

	q := "?select=key_id,key&" + kv("jid", s.JID) + "&uploaded=eq.false&order=key_id&limit=" + fmt.Sprint(count)
	rows, err := s.client.Select(ctx, "whatsmeow_pre_keys", q)
	if err != nil {
		return nil, err
	}
	newKeys := make([]*keys.PreKey, 0, len(rows))
	for _, r := range rows {
		priv := GetBytes(r, "key")
		if len(priv) != 32 {
			continue
		}
		newKeys = append(newKeys, &keys.PreKey{
			KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(priv)),
			KeyID:   uint32(GetInt(r, "key_id")),
		})
	}

	if uint32(len(newKeys)) < count {
		var nextID uint32
		nextID, err = s.nextPreKeyID(ctx)
		if err != nil {
			return nil, err
		}
		for i := uint32(len(newKeys)); i < count; i++ {
			key := keys.NewPreKey(nextID)
			if err := s.client.Insert(ctx, "whatsmeow_pre_keys", map[string]any{
				"jid": s.JID, "key_id": key.KeyID, "key": key.Priv[:], "uploaded": false,
			}); err != nil {
				return nil, err
			}
			newKeys = append(newKeys, key)
			nextID++
		}
	}
	return newKeys, nil
}

func (s *Store) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_pre_keys", "?select=key_id,key&"+kv("jid", s.JID)+"&"+kv("key_id", fmt.Sprint(id)))
	if err != nil || !ok {
		return nil, err
	}
	priv := GetBytes(row, "key")
	if len(priv) != 32 {
		return nil, errors.New("supabase: illegal prekey length")
	}
	return &keys.PreKey{KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(priv)), KeyID: id}, nil
}

func (s *Store) RemovePreKey(ctx context.Context, id uint32) error {
	q := "?" + kv("jid", s.JID) + "&" + kv("key_id", fmt.Sprint(id))
	return s.client.Delete(ctx, "whatsmeow_pre_keys", q)
}

func (s *Store) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	q := "?" + kv("jid", s.JID) + "&key_id=lte." + fmt.Sprint(upToID)
	return s.client.Update(ctx, "whatsmeow_pre_keys", q, map[string]any{"uploaded": true})
}

func (s *Store) UploadedPreKeyCount(ctx context.Context) (int, error) {
	rows, err := s.client.Select(ctx, "whatsmeow_pre_keys", "?select=count&"+kv("jid", s.JID)+"&uploaded=eq.true")
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return int(GetInt(rows[0], "count")), nil
}

// --- SenderKeyStore ---

func (s *Store) PutSenderKey(ctx context.Context, group, user string, session []byte) error {
	return s.client.Upsert(ctx, "whatsmeow_sender_keys", map[string]any{
		"our_jid": s.JID, "chat_id": group, "sender_id": user, "sender_key": session,
	}, []string{"our_jid", "chat_id", "sender_id"})
}

func (s *Store) GetSenderKey(ctx context.Context, group, user string) ([]byte, error) {
	q := "?select=sender_key&" + s.jidFilter() + "&" + kv("chat_id", group) + "&" + kv("sender_id", user)
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_sender_keys", q)
	if err != nil || !ok {
		return nil, err
	}
	return GetBytes(row, "sender_key"), nil
}

// --- AppStateSyncKeyStore ---

func (s *Store) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	return s.client.Upsert(ctx, "whatsmeow_app_state_sync_keys", map[string]any{
		"jid": s.JID, "key_id": id, "key_data": key.Data, "timestamp": key.Timestamp, "fingerprint": key.Fingerprint,
	}, []string{"jid", "key_id"})
}

func (s *Store) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	q := "?select=key_data,timestamp,fingerprint&" + kv("jid", s.JID) + "&order=timestamp.desc"
	rows, err := s.client.Select(ctx, "whatsmeow_app_state_sync_keys", q)
	if err != nil {
		return nil, err
	}
	out := make([]*store.AppStateSyncKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, &store.AppStateSyncKey{
			Data:        GetBytes(r, "key_data"),
			Fingerprint: GetBytes(r, "fingerprint"),
			Timestamp:   GetInt(r, "timestamp"),
		})
	}
	return out, nil
}

func (s *Store) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	q := "?select=key_data,timestamp,fingerprint&" + kv("jid", s.JID) + "&" + kv("key_id", string(id))
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_app_state_sync_keys", q)
	if err != nil || !ok {
		return nil, err
	}
	return &store.AppStateSyncKey{
		Data:        GetBytes(row, "key_data"),
		Fingerprint: GetBytes(row, "fingerprint"),
		Timestamp:   GetInt(row, "timestamp"),
	}, nil
}

func (s *Store) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	q := "?select=key_id&" + kv("jid", s.JID) + "&order=timestamp.desc&limit=1"
	rows, err := s.client.Select(ctx, "whatsmeow_app_state_sync_keys", q)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return GetBytes(rows[0], "key_id"), nil
}

// --- AppStateStore ---

func (s *Store) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	return s.client.Upsert(ctx, "whatsmeow_app_state_version", map[string]any{
		"jid": s.JID, "name": name, "version": version, "hash": hash[:],
	}, []string{"jid", "name"})
}

func (s *Store) GetAppStateVersion(ctx context.Context, name string) (uint64, [128]byte, error) {
	var hash [128]byte
	q := "?select=version,hash&" + kv("jid", s.JID) + "&" + kv("name", name)
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_app_state_version", q)
	if err != nil {
		return 0, hash, err
	}
	if !ok {
		return 0, hash, nil
	}
	v := uint64(GetInt(row, "version"))
	h := GetBytes(row, "hash")
	if len(h) == 128 {
		hash = *(*[128]byte)(h)
	}
	return v, hash, nil
}

func (s *Store) DeleteAppStateVersion(ctx context.Context, name string) error {
	q := "?" + kv("jid", s.JID) + "&" + kv("name", name)
	return s.client.Delete(ctx, "whatsmeow_app_state_version", q)
}

func (s *Store) PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []store.AppStateMutationMAC) error {
	for _, m := range mutations {
		if err := s.client.Upsert(ctx, "whatsmeow_app_state_mutation_macs", map[string]any{
			"jid": s.JID, "name": name, "version": version, "index_mac": m.IndexMAC, "value_mac": m.ValueMAC,
		}, []string{"jid", "name", "version", "index_mac"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error {
	for _, idx := range indexMACs {
		q := "?" + kv("jid", s.JID) + "&" + kv("name", name) + "&" + kv("index_mac", string(idx))
		if err := s.client.Delete(ctx, "whatsmeow_app_state_mutation_macs", q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) ([]byte, error) {
	q := "?select=value_mac&" + kv("jid", s.JID) + "&" + kv("name", name) + "&" + kv("index_mac", string(indexMAC)) + "&order=version.desc&limit=1"
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_app_state_mutation_macs", q)
	if err != nil || !ok {
		return nil, err
	}
	return GetBytes(row, "value_mac"), nil
}

// --- ContactStore ---

func (s *Store) getContact(ctx context.Context, user types.JID) (*types.ContactInfo, error) {
	s.contactCacheLock.Lock()
	defer s.contactCacheLock.Unlock()
	if cached, ok := s.contactCache[user]; ok {
		return cached, nil
	}
	q := "?select=first_name,full_name,push_name,business_name,redacted_phone&" + s.jidFilter() + "&" + kv("their_jid", user.ToNonAD().String())
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_contacts", q)
	if err != nil {
		return nil, err
	}
	info := &types.ContactInfo{}
	if ok {
		info.Found = true
		info.FirstName = GetString(row, "first_name")
		info.FullName = GetString(row, "full_name")
		info.PushName = GetString(row, "push_name")
		info.BusinessName = GetString(row, "business_name")
		info.RedactedPhone = GetString(row, "redacted_phone")
	}
	s.contactCache[user] = info
	return info, nil
}

func (s *Store) PutPushName(ctx context.Context, user types.JID, pushName string) (bool, string, error) {
	s.contactCacheLock.Lock()
	defer s.contactCacheLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return false, "", err
	}
	if cached.PushName != pushName {
		if err := s.client.Upsert(ctx, "whatsmeow_contacts", map[string]any{
			"our_jid": s.JID, "their_jid": user.ToNonAD().String(), "push_name": pushName,
		}, []string{"our_jid", "their_jid"}); err != nil {
			return false, "", err
		}
		prev := cached.PushName
		cached.PushName = pushName
		cached.Found = true
		return true, prev, nil
	}
	return false, "", nil
}

func (s *Store) PutBusinessName(ctx context.Context, user types.JID, businessName string) (bool, string, error) {
	s.contactCacheLock.Lock()
	defer s.contactCacheLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return false, "", err
	}
	if cached.BusinessName != businessName {
		if err := s.client.Upsert(ctx, "whatsmeow_contacts", map[string]any{
			"our_jid": s.JID, "their_jid": user.ToNonAD().String(), "business_name": businessName,
		}, []string{"our_jid", "their_jid"}); err != nil {
			return false, "", err
		}
		prev := cached.BusinessName
		cached.BusinessName = businessName
		cached.Found = true
		return true, prev, nil
	}
	return false, "", nil
}

func (s *Store) PutContactName(ctx context.Context, user types.JID, firstName, fullName string) error {
	s.contactCacheLock.Lock()
	defer s.contactCacheLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return err
	}
	if cached.FirstName != firstName || cached.FullName != fullName {
		if err := s.client.Upsert(ctx, "whatsmeow_contacts", map[string]any{
			"our_jid": s.JID, "their_jid": user.ToNonAD().String(), "first_name": firstName, "full_name": fullName,
		}, []string{"our_jid", "their_jid"}); err != nil {
			return err
		}
		cached.FirstName, cached.FullName = firstName, fullName
		cached.Found = true
	}
	return nil
}

func (s *Store) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	for _, c := range contacts {
		if err := s.client.Upsert(ctx, "whatsmeow_contacts", map[string]any{
			"our_jid": s.JID, "their_jid": c.JID.ToNonAD().String(), "first_name": c.FirstName, "full_name": c.FullName,
		}, []string{"our_jid", "their_jid"}); err != nil {
			return err
		}
	}
	s.contactCacheLock.Lock()
	s.contactCache = make(map[types.JID]*types.ContactInfo)
	s.contactCacheLock.Unlock()
	return nil
}

func (s *Store) PutManyRedactedPhones(ctx context.Context, entries []store.RedactedPhoneEntry) error {
	for _, e := range entries {
		if err := s.client.Upsert(ctx, "whatsmeow_contacts", map[string]any{
			"our_jid": s.JID, "their_jid": e.JID.ToNonAD().String(), "redacted_phone": e.RedactedPhone,
		}, []string{"our_jid", "their_jid"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	s.contactCacheLock.Lock()
	s.contactCacheLock.Unlock()
	cached, err := s.getContact(ctx, user)
	if err != nil {
		return types.ContactInfo{}, err
	}
	return *cached, nil
}

func (s *Store) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	q := "?select=their_jid,first_name,full_name,push_name,business_name,redacted_phone&" + s.jidFilter()
	rows, err := s.client.Select(ctx, "whatsmeow_contacts", q)
	if err != nil {
		return nil, err
	}
	out := make(map[types.JID]types.ContactInfo, len(rows))
	for _, r := range rows {
		jid, err := types.ParseJID(GetString(r, "their_jid"))
		if err != nil {
			continue
		}
		out[jid] = types.ContactInfo{
			Found:         true,
			FirstName:     GetString(r, "first_name"),
			FullName:      GetString(r, "full_name"),
			PushName:      GetString(r, "push_name"),
			BusinessName:  GetString(r, "business_name"),
			RedactedPhone: GetString(r, "redacted_phone"),
		}
	}
	return out, nil
}

// --- ChatSettingsStore ---

func (s *Store) PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error {
	var val int64
	if mutedUntil == store.MutedForever {
		val = -1
	} else if !mutedUntil.IsZero() {
		val = mutedUntil.Unix()
	}
	return s.client.Upsert(ctx, "whatsmeow_chat_settings", map[string]any{
		"our_jid": s.JID, "chat_jid": chat.String(), "muted_until": val,
	}, []string{"our_jid", "chat_jid"})
}

func (s *Store) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	return s.client.Upsert(ctx, "whatsmeow_chat_settings", map[string]any{
		"our_jid": s.JID, "chat_jid": chat.String(), "pinned": pinned,
	}, []string{"our_jid", "chat_jid"})
}

func (s *Store) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	return s.client.Upsert(ctx, "whatsmeow_chat_settings", map[string]any{
		"our_jid": s.JID, "chat_jid": chat.String(), "archived": archived,
	}, []string{"our_jid", "chat_jid"})
}

func (s *Store) GetChatSettings(ctx context.Context, chat types.JID) (types.LocalChatSettings, error) {
	var settings types.LocalChatSettings
	q := "?select=muted_until,pinned,archived&" + s.jidFilter() + "&" + kv("chat_jid", chat.String())
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_chat_settings", q)
	if err != nil {
		return settings, err
	}
	if ok {
		settings.Found = true
		settings.Pinned = GetBool(row, "pinned")
		settings.Archived = GetBool(row, "archived")
		muted := GetInt(row, "muted_until")
		if muted < 0 {
			settings.MutedUntil = store.MutedForever
		} else if muted > 0 {
			settings.MutedUntil = time.Unix(muted, 0)
		}
	}
	return settings, nil
}

// --- MsgSecretStore ---

func (s *Store) PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	return s.client.InsertIgnore(ctx, "whatsmeow_message_secrets", map[string]any{
		"our_jid": s.JID, "chat_jid": chat.ToNonAD().String(), "sender_jid": sender.ToNonAD().String(),
		"message_id": string(id), "key": secret,
	})
}

func (s *Store) PutMessageSecrets(ctx context.Context, inserts []store.MessageSecretInsert) error {
	for _, ins := range inserts {
		if err := s.PutMessageSecret(ctx, ins.Chat, ins.Sender, ins.ID, ins.Secret); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) ([]byte, types.JID, error) {
	q := "?select=key,sender_jid&" + s.jidFilter() + "&" + kv("chat_jid", chat.ToNonAD().String()) +
		"&" + kv("sender_jid", sender.ToNonAD().String()) + "&" + kv("message_id", id)
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_message_secrets", q)
	if err != nil {
		return nil, types.EmptyJID, err
	}
	if !ok {
		return nil, types.EmptyJID, nil
	}
	realSender, _ := types.ParseJID(GetString(row, "sender_jid"))
	return GetBytes(row, "key"), realSender, nil
}

// --- PrivacyTokenStore ---

func (s *Store) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	for _, t := range tokens {
		var senderTS any
		if !t.SenderTimestamp.IsZero() {
			senderTS = t.SenderTimestamp.Unix()
		}
		if err := s.client.Upsert(ctx, "whatsmeow_privacy_tokens", map[string]any{
			"our_jid": s.JID, "their_jid": t.User.ToNonAD().String(), "token": t.Token,
			"timestamp": t.Timestamp.Unix(), "sender_timestamp": senderTS,
		}, []string{"our_jid", "their_jid"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetPrivacyToken(ctx context.Context, user types.JID) (*store.PrivacyToken, error) {
	q := "?select=token,timestamp,sender_timestamp&" + s.jidFilter() + "&" + kv("their_jid", user.ToNonAD().String()) + "&order=timestamp.desc&limit=1"
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_privacy_tokens", q)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	token := &store.PrivacyToken{
		User:      user.ToNonAD(),
		Token:     GetBytes(row, "token"),
		Timestamp: time.Unix(GetInt(row, "timestamp"), 0),
	}
	if st := GetInt(row, "sender_timestamp"); st != 0 {
		token.SenderTimestamp = time.Unix(st, 0)
	}
	return token, nil
}

func (s *Store) DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	q := "?" + s.jidFilter() + "&timestamp=lt." + fmt.Sprint(cutoff.Unix())
	return 0, s.client.Delete(ctx, "whatsmeow_privacy_tokens", q)
}

// --- NCTSaltStore ---

func (s *Store) PutNCTSalt(ctx context.Context, salt []byte) error {
	return s.client.Upsert(ctx, "whatsmeow_nct_salt", map[string]any{"our_jid": s.JID, "salt": salt}, []string{"our_jid"})
}

func (s *Store) GetNCTSalt(ctx context.Context) ([]byte, error) {
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_nct_salt", "?select=salt&"+kv("our_jid", s.JID))
	if err != nil || !ok {
		return nil, err
	}
	return GetBytes(row, "salt"), nil
}

func (s *Store) DeleteNCTSalt(ctx context.Context) error {
	return s.client.Delete(ctx, "whatsmeow_nct_salt", "?"+kv("our_jid", s.JID))
}

// --- EventBuffer ---

func (s *Store) GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*store.BufferedEvent, error) {
	q := "?select=plaintext,server_timestamp,insert_timestamp&" + s.jidFilter() + "&" + kv("ciphertext_hash", string(ciphertextHash[:]))
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_event_buffer", q)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &store.BufferedEvent{
		Plaintext:  GetBytes(row, "plaintext"),
		ServerTime: time.Unix(GetInt(row, "server_timestamp"), 0),
		InsertTime: time.UnixMilli(GetInt(row, "insert_timestamp")),
	}, nil
}

func (s *Store) PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error {
	return s.client.InsertIgnore(ctx, "whatsmeow_event_buffer", map[string]any{
		"our_jid": s.JID, "ciphertext_hash": ciphertextHash[:], "plaintext": plaintext,
		"server_timestamp": serverTimestamp.Unix(), "insert_timestamp": time.Now().UnixMilli(),
	})
}

func (s *Store) DoDecryptionTxn(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *Store) ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error {
	q := "?" + s.jidFilter() + "&" + kv("ciphertext_hash", string(ciphertextHash[:]))
	return s.client.Update(ctx, "whatsmeow_event_buffer", q, map[string]any{"plaintext": nil})
}

func (s *Store) DeleteOldBufferedHashes(ctx context.Context) error {
	cutoff := time.Now().Add(-14 * 24 * time.Hour).UnixMilli()
	q := "?" + s.jidFilter() + "&insert_timestamp=lt." + fmt.Sprint(cutoff)
	return s.client.Delete(ctx, "whatsmeow_event_buffer", q)
}

func (s *Store) GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (string, []byte, error) {
	q := "?select=format,plaintext&" + s.jidFilter() + "&or=(chat_jid.eq." + url.QueryEscape(chatJID.String()) +
		",chat_jid.eq." + url.QueryEscape(altChatJID.String()) + ")&" + kv("message_id", id)
	row, ok, err := s.client.SelectOne(ctx, "whatsmeow_retry_buffer", q)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, nil
	}
	return GetString(row, "format"), GetBytes(row, "plaintext"), nil
}

func (s *Store) AddOutgoingEvent(ctx context.Context, chatJID types.JID, id types.MessageID, format string, plaintext []byte) error {
	return s.client.Upsert(ctx, "whatsmeow_retry_buffer", map[string]any{
		"our_jid": s.JID, "chat_jid": chatJID.String(), "message_id": string(id),
		"format": format, "plaintext": plaintext, "timestamp": time.Now().UnixMilli(),
	}, []string{"our_jid", "chat_jid", "message_id"})
}

func (s *Store) DeleteOldOutgoingEvents(ctx context.Context) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	q := "?" + s.jidFilter() + "&timestamp=lt." + fmt.Sprint(cutoff)
	return s.client.Delete(ctx, "whatsmeow_retry_buffer", q)
}

var _ = sort.Ints

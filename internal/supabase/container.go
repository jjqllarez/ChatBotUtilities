package supabase

import (
	"context"
	"errors"
	"fmt"
	mathRand "math/rand/v2"

	"github.com/google/uuid"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// Container is a whatsmeow device container backed by Supabase via REST.
type Container struct {
	client *Client
	log    waLog.Logger
	LIDMap *LIDMap
}

var _ store.DeviceContainer = (*Container)(nil)

func NewContainer(client *Client, log waLog.Logger) *Container {
	if log == nil {
		log = waLog.Noop
	}
	c := &Container{client: client, log: log}
	c.LIDMap = NewLIDMap(c)
	return c
}

func (c *Container) Client() *Client { return c.client }

// NewDevice creates a new device (not persisted until Save is called).
func (c *Container) NewDevice() *store.Device {
	device := &store.Device{
		Log:       c.log,
		Container: c,

		NoiseKey:       keys.NewKeyPair(),
		IdentityKey:    keys.NewKeyPair(),
		RegistrationID: mathRand.Uint32(),
		AdvSecretKey:   random.Bytes(32),
	}
	device.SignedPreKey = device.IdentityKey.CreateSignedPreKey(1)
	return device
}

// PutDevice persists a device row.
func (c *Container) PutDevice(ctx context.Context, device *store.Device) error {
	if device.ID == nil {
		return ErrDeviceIDMustBeSet
	}
	row := map[string]any{
		"jid":                  device.ID.String(),
		"lid":                  device.LID.String(),
		"registration_id":      device.RegistrationID,
		"noise_key":            device.NoiseKey.Priv[:],
		"identity_key":         device.IdentityKey.Priv[:],
		"signed_pre_key":       device.SignedPreKey.Priv[:],
		"signed_pre_key_id":    device.SignedPreKey.KeyID,
		"signed_pre_key_sig":   device.SignedPreKey.Signature[:],
		"adv_key":              device.AdvSecretKey,
		"adv_details":          device.Account.Details,
		"adv_account_sig":      device.Account.AccountSignature,
		"adv_account_sig_key":  device.Account.AccountSignatureKey,
		"adv_device_sig":       device.Account.DeviceSignature,
		"platform":             device.Platform,
		"business_name":        device.BusinessName,
		"push_name":            device.PushName,
		"lid_migration_ts":     device.LIDMigrationTimestamp,
		"companion_meta_nonce": device.CompanionMetaNonce,
	}
	if device.FacebookUUID != uuid.Nil {
		row["facebook_uuid"] = device.FacebookUUID.String()
	}
	if err := c.client.Upsert(ctx, "whatsmeow_device", row, []string{"jid"}); err != nil {
		return err
	}
	if !device.Initialized {
		c.initializeDevice(device)
	}
	return nil
}

// DeleteDevice removes a device row.
func (c *Container) DeleteDevice(ctx context.Context, device *store.Device) error {
	if device.ID == nil {
		return ErrDeviceIDMustBeSet
	}
	return c.client.Delete(ctx, "whatsmeow_device", "?"+kv("jid", device.ID.String()))
}

func (c *Container) initializeDevice(device *store.Device) {
	inner := NewStore(c, *device.ID)
	device.SetAllStores(inner)
	device.LIDs = c.LIDMap
	device.Container = c
	device.Initialized = true
}

// GetAllDevices loads all persisted devices.
func (c *Container) GetAllDevices(ctx context.Context) ([]*store.Device, error) {
	rows, err := c.client.Select(ctx, "whatsmeow_device", "?select=*")
	if err != nil {
		return nil, err
	}
	devices := make([]*store.Device, 0, len(rows))
	for _, r := range rows {
		d, err := c.scanDevice(r)
		if err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// GetDevice loads a single device by JID; returns nil if not found.
func (c *Container) GetDevice(ctx context.Context, jid types.JID) (*store.Device, error) {
	row, ok, err := c.client.SelectOne(ctx, "whatsmeow_device", "?select=*&"+kv("jid", jid.String()))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return c.scanDevice(row)
}

func (c *Container) scanDevice(r map[string]any) (*store.Device, error) {
	var device store.Device
	device.Log = c.log
	device.SignedPreKey = &keys.PreKey{}

	noisePriv := GetBytes(r, "noise_key")
	identityPriv := GetBytes(r, "identity_key")
	preKeyPriv := GetBytes(r, "signed_pre_key")
	preKeySig := GetBytes(r, "signed_pre_key_sig")
	if len(noisePriv) != 32 || len(identityPriv) != 32 || len(preKeyPriv) != 32 || len(preKeySig) != 64 {
		return nil, errors.New("supabase: device row has illegal byte lengths")
	}

	jidStr := GetString(r, "jid")
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, fmt.Errorf("supabase: invalid device jid %q: %w", jidStr, err)
	}
	device.ID = &jid

	if lidStr := GetString(r, "lid"); lidStr != "" {
		if lid, err := types.ParseJID(lidStr); err == nil {
			device.LID = lid
		}
	}
	if fb := GetString(r, "facebook_uuid"); fb != "" {
		if u, err := uuid.Parse(fb); err == nil {
			device.FacebookUUID = u
		}
	}

	device.RegistrationID = uint32(GetInt(r, "registration_id"))
	device.NoiseKey = keys.NewKeyPairFromPrivateKey(*(*[32]byte)(noisePriv))
	device.IdentityKey = keys.NewKeyPairFromPrivateKey(*(*[32]byte)(identityPriv))
	device.SignedPreKey.KeyPair = *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(preKeyPriv))
	device.SignedPreKey.KeyID = uint32(GetInt(r, "signed_pre_key_id"))
	device.SignedPreKey.Signature = (*[64]byte)(preKeySig)

	device.AdvSecretKey = GetBytes(r, "adv_key")
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             GetBytes(r, "adv_details"),
		AccountSignature:    GetBytes(r, "adv_account_sig"),
		AccountSignatureKey: GetBytes(r, "adv_account_sig_key"),
		DeviceSignature:     GetBytes(r, "adv_device_sig"),
	}

	device.Platform = GetString(r, "platform")
	device.BusinessName = GetString(r, "business_name")
	device.PushName = GetString(r, "push_name")
	device.LIDMigrationTimestamp = GetInt(r, "lid_migration_ts")
	device.CompanionMetaNonce = GetString(r, "companion_meta_nonce")

	c.initializeDevice(&device)
	return &device, nil
}

// ErrDeviceIDMustBeSet is returned when saving a device without a known JID.
var ErrDeviceIDMustBeSet = errors.New("device JID must be known before accessing database")

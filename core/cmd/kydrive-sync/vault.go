package main

import (
	"context"
	"errors"
	"time"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/Busnes-app/kydrive-desktop/core/vault"
	"github.com/Busnes-app/kydrive-server/pkg/vaultclient"
)

type VaultArgs struct {
	AccountID string
	Action    string
	Witness   vault.Config
	Invite    vaultclient.Invitation
}

// VaultStatus contains public pins and catalogue metadata only. The native editor
// stays in the app's private process boundary.
type VaultStatus struct {
	Member     vaultclient.MemberPin
	Invitation vaultclient.Invitation
	Generation int64
}

func (c *AccountsCore) Vault(in VaultArgs, out *VaultStatus) error {
	core, err := c.m.resolve(in.AccountID)
	if err != nil {
		return err
	}
	return core.Vault(in, out)
}

func (c *Core) Vault(in VaultArgs, out *VaultStatus) error {
	switch in.Action {
	case "identity", "create", "verify":
	default:
		return errors.New("choose vault action identity, create or verify")
	}
	d := c.d
	unlock := d.lockWork()
	defer unlock()
	d.mu.Lock()
	drive := api.Client{Base: d.db.Get("server"), Token: d.token, Key: d.key, HTTP: d.http, Pins: d.session()}
	account := d.account()
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 45*time.Second)
	defer cancel()
	v, err := vault.Connect(ctx, drive, account, in.Witness)
	if err != nil {
		return err
	}
	defer v.Close()
	out.Member = v.Member()
	switch in.Action {
	case "create":
		invite, err := v.CreateVault(ctx)
		out.Invitation = invite
		if err != nil && invite.Vault != "" {
			// A lost commit/witness response can leave the vault committed. Surface
			// its public invitation so the same vault can be verified later.
			return errors.Join(err, errors.New("try verifying vault "+invite.Vault+" with your member fingerprint"))
		}
		if err == nil {
			out.Generation = 1
		}
		return err
	case "verify":
		state, err := v.OpenVault(ctx, in.Invite)
		if err != nil {
			return err
		}
		out.Invitation, out.Generation = in.Invite, state.Generation
	}
	return nil
}

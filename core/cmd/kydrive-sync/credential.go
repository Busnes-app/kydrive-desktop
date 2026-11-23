package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/Busnes-app/kydrive-desktop/core/api"
	"github.com/zalando/go-keyring"
)

// The keyring item is space-separated: the device token; then, for a computer linked with a
// signing key, the key as std base64 PKCS#8 DER ("-" for none); then, once a TLS leaf is
// pinned, the pin, the server's identity key ("-" until learned) and the pin's freshness floor
// (Unix seconds). One item, so all of it is stored and deleted together. A software key the
// keyring guards; a platform key that cannot be exported (TPM, Secure Enclave) would be stronger.

var errCredentialUnread = errors.New("the saved KyDrive credential is unavailable; unlock or restore the OS keyring and wait for recovery before signing in again, or unlink and link again")

var errDamaged = errors.New("the keyring's KyDrive credential is damaged; sign in again")

func storeCredential(server, token string, key *ecdsa.PrivateKey, pin api.Pin) error {
	if token == "" {
		if key != nil {
			return errDamaged
		}
		token = "-" // revoked: retain only the held pin (or known legacy absence of one)
	}
	v := token
	if key != nil || pin.Leaf != "" {
		enc := "-"
		if key != nil {
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				return err
			}
			enc = base64.StdEncoding.EncodeToString(der)
		}
		v += " " + enc
	}
	if pin.Leaf != "" {
		v += " " + pin.Leaf + " " + dash(pin.IdentityKey) + " " + strconv.FormatInt(pin.Floor, 10)
	}
	return keyring.Set(keyringService, server, v)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// loadCredential returns a nil key for a computer linked before signing and a zero pin for one
// that has not pinned a leaf yet. A token field of "-" is a revoked, pin-only
// record: it returns no token or key, including for known unpinned legacy links.
func loadCredential(server string) (string, *ecdsa.PrivateKey, api.Pin, error) {
	v, err := keyring.Get(keyringService, server)
	if err != nil {
		return "", nil, api.Pin{}, err
	}
	f := strings.Split(v, " ")
	if len(f) != 1 && len(f) != 2 && len(f) != 5 || f[0] == "" {
		return "", nil, api.Pin{}, errDamaged
	}
	var key *ecdsa.PrivateKey
	if len(f) > 1 && f[1] != "-" {
		der, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil {
			return "", nil, api.Pin{}, errDamaged
		}
		k, err := x509.ParsePKCS8PrivateKey(der)
		var ok bool
		if key, ok = k.(*ecdsa.PrivateKey); err != nil || !ok {
			return "", nil, api.Pin{}, errDamaged
		}
	}
	var pin api.Pin
	if len(f) == 5 {
		floor, err := strconv.ParseInt(f[4], 10, 64)
		if err != nil || !strings.HasPrefix(f[2], "sha256/") {
			return "", nil, api.Pin{}, errDamaged
		}
		pin = api.Pin{Leaf: f[2], Floor: floor}
		if f[3] != "-" {
			pin.IdentityKey = f[3]
		}
	}
	if f[0] == "-" {
		if key != nil {
			return "", nil, api.Pin{}, errDamaged
		}
		return "", nil, pin, nil
	}
	return f[0], key, pin, nil
}

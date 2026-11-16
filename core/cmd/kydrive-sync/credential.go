package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

// The keyring item holds the device token and, for a computer linked with a signing key, a
// space and the key as std base64 PKCS#8 DER. One item, so the two are stored and deleted
// together. A software key the keyring guards; a platform key that cannot be exported
// (TPM, Secure Enclave) would be stronger.

func storeCredential(server, token string, key *ecdsa.PrivateKey) error {
	v := token
	if key != nil {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		v += " " + base64.StdEncoding.EncodeToString(der)
	}
	return keyring.Set(keyringService, server, v)
}

// loadCredential returns a nil key for a computer linked before signing.
func loadCredential(server string) (string, *ecdsa.PrivateKey, error) {
	v, err := keyring.Get(keyringService, server)
	if err != nil {
		return "", nil, err
	}
	token, enc, signed := strings.Cut(v, " ")
	if !signed {
		return token, nil, nil
	}
	der, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", nil, errors.New("the keyring's KyDrive signing key is damaged; sign in again")
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	key, ok := k.(*ecdsa.PrivateKey)
	if err != nil || !ok {
		return "", nil, errors.New("the keyring's KyDrive signing key is damaged; sign in again")
	}
	return token, key, nil
}

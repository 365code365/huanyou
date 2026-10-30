// Package keys generates REALITY key pairs and random secrets.
package keys

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
)

// RealityKeys generates an X25519 key pair encoded the way Xray expects
// (base64 raw URL encoding), plus a random 8-byte short ID.
func RealityKeys() (priv, pub, shortID string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	sid := make([]byte, 8)
	if _, err := rand.Read(sid); err != nil {
		return "", "", "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(k.Bytes()), enc.EncodeToString(k.PublicKey().Bytes()), hex.EncodeToString(sid), nil
}

// RandomToken returns a URL-safe random secret.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ShortID returns a random 8-byte REALITY short id (hex).
func ShortID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

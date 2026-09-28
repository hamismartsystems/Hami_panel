package reality

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"crypto/ecdh"
)

// GenerateKeypair creates a new X25519 keypair for Reality.
// The private key is what Xray needs, the public key is what goes into the link.
func GenerateKeypair() (private, public string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	priv := base64.RawURLEncoding.EncodeToString(k.Bytes())
	pub := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	return priv, pub, nil
}

// GenerateShortID creates a random short ID of the given byte length
// (Xray expects 0-8 bytes hex, usually 8 chars = 4 bytes).
func GenerateShortID(bytesLen int) (string, error) {
	if bytesLen <= 0 {
		bytesLen = 4
	}
	if bytesLen > 8 {
		bytesLen = 8
	}
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// hex encoding
	const hex = "0123456789abcdef"
	out := make([]byte, bytesLen*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out), nil
}

// MustGenerateKeypair panics on error, convenient for templates.
func MustGenerateKeypair() (string, string) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		panic(fmt.Sprintf("reality key: %v", err))
	}
	return priv, pub
}

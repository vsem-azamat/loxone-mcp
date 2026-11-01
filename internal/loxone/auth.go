package loxone

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the Miniserver chooses the algorithm; older users still get SHA1
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"
	"time"
)

// epoch2009 is the Miniserver's epoch: its timestamps count seconds since 2009-01-01.
var epoch2009 = time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC)

// FromEpoch2009 converts Miniserver seconds since 2009 to a time.
func FromEpoch2009(sec int64) time.Time { return epoch2009.Add(time.Duration(sec) * time.Second) }

// ToEpoch2009 converts a time to Miniserver seconds since 2009.
func ToEpoch2009(t time.Time) int64 { return int64(t.Sub(epoch2009) / time.Second) }

func hashFunc(alg string) (func() hash.Hash, error) {
	switch strings.ToUpper(alg) {
	case "SHA256":
		return sha256.New, nil
	case "SHA1", "":
		return sha1.New, nil
	default:
		return nil, fmt.Errorf("unsupported hash algorithm %q", alg)
	}
}

// keyInfo is the answer to getkey2: the one-time key, the user's salt and the hash algorithm.
type keyInfo struct {
	Key     string `json:"key"`
	Salt    string `json:"salt"`
	HashAlg string `json:"hashAlg"`
}

// passwordHash is the hash getjwt expects: HMAC(key, "user:" + upper(hex(H(password:salt)))).
func passwordHash(ki keyInfo, user, password string) (string, error) {
	h, err := hashFunc(ki.HashAlg)
	if err != nil {
		return "", err
	}
	pw := h()
	pw.Write([]byte(password + ":" + ki.Salt))
	pwHash := strings.ToUpper(hex.EncodeToString(pw.Sum(nil)))
	return hmacHex(h, ki.Key, user+":"+pwHash)
}

// tokenHash is HMAC(key, token) with the one-time key of getkey.
func tokenHash(alg, key, token string) (string, error) {
	h, err := hashFunc(alg)
	if err != nil {
		return "", err
	}
	return hmacHex(h, key, token)
}

func hmacHex(h func() hash.Hash, hexKey, msg string) (string, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return "", fmt.Errorf("key is not hex: %w", err)
	}
	m := hmac.New(h, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// jwtAnswer is the value of a getjwt or refreshjwt response.
type jwtAnswer struct {
	Token        string `json:"token"`
	ValidUntil   int64  `json:"validUntil"`
	TokenRights  int64  `json:"tokenRights"`
	UnsecurePass bool   `json:"unsecurePass"`
}

func decodeJSONValue(raw json.RawMessage, v any) error {
	// Some answers carry the JSON object as a string, others inline.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		raw = json.RawMessage(s)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	return nil
}

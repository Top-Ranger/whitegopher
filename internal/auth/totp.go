package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random shared secret for an authenticator app.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// OTPAuthURI is the otpauth:// URI that authenticator apps import.
func OTPAuthURI(issuer, account, secret string) string {
	v := url.Values{"secret": {secret}, "issuer": {issuer}}
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + v.Encode()
}

// totpAt computes the 6-digit RFC 6238 code (HMAC-SHA1, 30 s steps) for a step.
func totpAt(secret string, step int64) (string, bool) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil || len(key) == 0 {
		return "", false
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1000000), true
}

// ValidTOTP checks code against the secret for the current step and one step
// either side (clock drift). Steps at or before last are refused so a code
// cannot be used twice; the matching step is returned.
func ValidTOTP(secret, code string, now time.Time, last int64) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		s := cur + d
		if s <= last {
			continue
		}
		if want, ok := totpAt(secret, s); ok && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

// NewRecoveryCodes returns n random one-time codes like "k3j9d-x2m4q" and
// their stored hashes.
func NewRecoveryCodes(n int) (codes, hashes []string, err error) {
	for i := 0; i < n; i++ {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		s := strings.ToLower(b32.EncodeToString(b))[:10]
		c := s[:5] + "-" + s[5:]
		codes = append(codes, c)
		hashes = append(hashes, HashRecoveryCode(c))
	}
	return codes, hashes, nil
}

// HashRecoveryCode normalises (case, dashes, spaces) and hashes a recovery code.
func HashRecoveryCode(code string) string {
	code = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))
	sum := sha256.Sum256([]byte("recovery:" + code))
	return hex.EncodeToString(sum[:])
}

// TOTPCode returns the authenticator code valid at t.
func TOTPCode(secret string, t time.Time) (string, bool) {
	return totpAt(secret, t.Unix()/30)
}

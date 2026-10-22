// Package auth verifies credentials and guards HTTP handlers.
package auth

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/bitflipp/galdav/internal/store"
)

// HA1 returns md5(user:realm:password), the hash Baikal stores. It is only
// used to verify imported accounts once; see Verify.
func HA1(user, realm, password string) string {
	sum := md5.Sum([]byte(user + ":" + realm + ":" + password))
	return hex.EncodeToString(sum[:])
}

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyArgon(phc, password string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Authenticator checks Basic credentials against the store.
type Authenticator struct {
	Store *store.Store
	Realm string
}

// Verify checks a password. Accounts that only have an imported HA1 are
// verified against it and upgraded to an argon2id hash on success.
func (a *Authenticator) Verify(username, password string) (store.User, bool) {
	u, err := a.Store.UserByName(username)
	if err != nil {
		// Burn comparable time so unknown users aren't distinguishable.
		verifyArgon("$argon2id$v=19$m=65536,t=2,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		return u, false
	}
	if u.PasswordHash != "" {
		return u, verifyArgon(u.PasswordHash, password)
	}
	if u.DigestHA1 == "" {
		return u, false
	}
	if subtle.ConstantTimeCompare([]byte(HA1(username, a.Realm, password)), []byte(u.DigestHA1)) != 1 {
		return u, false
	}
	if h, err := HashPassword(password); err == nil {
		_ = a.Store.SetPasswordHash(u.ID, h) // best-effort upgrade; also clears the HA1
	}
	return u, true
}

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ValidUsername reports whether name is acceptable for a new account. The
// name appears in DAV URLs, so it is restricted to URL-safe characters.
func ValidUsername(name string) bool { return usernameRE.MatchString(name) }

type ctxKey struct{}

// UserFrom returns the authenticated user set by Middleware.
func UserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(store.User)
	return u, ok
}

// Middleware requires HTTP Basic credentials.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, pass, ok := r.BasicAuth(); ok {
			if u, good := a.Verify(name, pass); good {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
				return
			}
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q, charset="UTF-8"`, a.Realm))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

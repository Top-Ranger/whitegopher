// Package auth verifies credentials and guards HTTP handlers.
package auth

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/Top-Ranger/whitegopher/internal/netx"
	"github.com/Top-Ranger/whitegopher/internal/obs"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

// HA1 returns md5(user:realm:password), the legacy hash an import may carry. It is only
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
	// Proxies and Limiter are optional. Proxies decide which X-Forwarded-For
	// is believed when attributing failed logins; Limiter throttles them per
	// client address.
	Proxies netx.Proxies
	Limiter *netx.Limiter
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

// AppPasswordPrefix starts every app password, so they are recognisable (and
// can be told apart from account passwords without a database lookup).
const AppPasswordPrefix = "gdp-"

// NewAppPassword returns a random per-device password such as
// "gdp-abcde-fghij-klmno-pqrst" and the hash to store for it.
func NewAppPassword() (token, hash string, err error) {
	b := make([]byte, 13)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	s := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:20]
	token = AppPasswordPrefix + s[0:5] + "-" + s[5:10] + "-" + s[10:15] + "-" + s[15:20]
	return token, HashAppPassword(token), nil
}

// HashAppPassword is the stored form of an app password. App passwords are
// long and random, so a fast hash is enough and keeps every DAV request cheap.
func HashAppPassword(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyDAV checks the credentials of a DAV request: an app password of the
// user, or their account password. DAV clients cannot answer a second-factor
// prompt, so once two-factor is on, only app passwords are accepted.
func (a *Authenticator) VerifyDAV(username, password string) (store.User, bool) {
	if u, err := a.Store.UserByName(username); err == nil {
		if strings.HasPrefix(password, AppPasswordPrefix) {
			if ok, _ := a.Store.UseAppPassword(u.ID, HashAppPassword(password)); ok {
				return u, true
			}
		}
		if u.TOTPEnabled {
			return u, false
		}
	}
	return a.Verify(username, password)
}

// VerifyLogin checks an admin UI sign-in: the account password and, when the
// user has two-factor on, an authenticator or recovery code. passwordOK says
// whether the password alone was right, so callers can throttle code guesses.
func (a *Authenticator) VerifyLogin(username, password, code string) (u store.User, ok, passwordOK bool) {
	u, passwordOK = a.Verify(username, password)
	if !passwordOK {
		return u, false, false
	}
	if u.TOTPEnabled && !a.CheckSecondFactor(u, code) {
		return u, false, true
	}
	return u, true, true
}

// CheckSecondFactor verifies an authenticator code (once per time step) or a
// recovery code (once ever) for a user who has two-factor on.
func (a *Authenticator) CheckSecondFactor(u store.User, code string) bool {
	if step, ok := ValidTOTP(u.TOTPSecret, code, time.Now(), u.TOTPLastStep); ok {
		fresh, err := a.Store.AdvanceTOTPStep(u.ID, step)
		return err == nil && fresh
	}
	ok, _ := a.Store.UseRecoveryCode(u.ID, HashRecoveryCode(code))
	return ok
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

// Middleware requires HTTP Basic credentials. Failed attempts are logged with
// the client address and count towards the rate limit.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := a.Proxies.ClientIP(r)
		if !a.Limiter.Allowed(ip) {
			slog.Warn("login blocked", "via", "dav", "ip", ip)
			obs.LoginsBlocked.Inc()
			w.Header().Set("Retry-After", "900")
			http.Error(w, "too many failed login attempts", http.StatusTooManyRequests)
			return
		}
		if name, pass, ok := r.BasicAuth(); ok {
			u, good := a.VerifyDAV(name, pass)
			if good {
				a.Limiter.Reset(ip)
				obs.SetUser(r.Context(), u.Username)
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
				return
			}
			a.Limiter.Fail(ip)
			obs.LoginFailures.Inc()
			slog.Warn("login failed", "via", "dav", "user", name, "ip", ip)
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q, charset="UTF-8"`, a.Realm))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

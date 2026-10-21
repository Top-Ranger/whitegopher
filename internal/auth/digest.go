package auth

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bitflipp/galdav/internal/store"
)

const nonceLifetime = 5 * time.Minute

// digestState holds the server-side state of Digest authentication: the
// secret that signs nonces, and the highest nonce count seen per nonce to
// reject replays.
type digestState struct {
	once   sync.Once
	secret []byte
	mu     sync.Mutex
	seen   map[string]uint64
}

func (d *digestState) init() {
	d.once.Do(func() {
		d.secret = make([]byte, 32)
		if _, err := rand.Read(d.secret); err != nil {
			panic(err)
		}
		d.seen = map[string]uint64{}
	})
}

func (d *digestState) sign(ts string) string {
	m := hmac.New(sha256.New, d.secret)
	m.Write([]byte(ts))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func (d *digestState) newNonce() string {
	d.init()
	// The random part makes nonces unique even within one second; replay
	// protection tracks nonce counts per nonce.
	r := make([]byte, 8)
	if _, err := rand.Read(r); err != nil {
		panic(err)
	}
	body := strconv.FormatInt(time.Now().Unix(), 16) + "-" + hex.EncodeToString(r)
	return body + "." + d.sign(body)
}

// checkNonce reports whether nonce is genuine, and whether it has expired.
func (d *digestState) checkNonce(nonce string) (valid, stale bool) {
	d.init()
	body, sig, ok := strings.Cut(nonce, ".")
	if !ok || subtle.ConstantTimeCompare([]byte(sig), []byte(d.sign(body))) != 1 {
		return false, false
	}
	ts, _, _ := strings.Cut(body, "-")
	secs, err := strconv.ParseInt(ts, 16, 64)
	if err != nil {
		return false, false
	}
	return true, time.Since(time.Unix(secs, 0)) > nonceLifetime
}

// useNonceCount records nc for nonce and reports whether it is fresh.
func (d *digestState) useNonceCount(nonce string, nc uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.seen) > 10000 { // drop entries whose nonces have expired anyway
		for n := range d.seen {
			if _, stale := d.checkNonce(n); stale {
				delete(d.seen, n)
			}
		}
	}
	if nc <= d.seen[nonce] {
		return false
	}
	d.seen[nonce] = nc
	return true
}

// parseAuthParams parses the key=value list of an Authorization header.
func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, ", \t")
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = s[eq+1:]
		var val string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			val = b.String()
			if i < len(s) {
				i++
			}
			s = s[i:]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			val = strings.TrimSpace(s[:end])
			s = s[end:]
		}
		out[key] = val
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DigestResponse computes the request-digest for qop=auth (or RFC 2069 when
// qop is empty).
func DigestResponse(ha1, method, uri, nonce, nc, cnonce, qop string) string {
	ha2 := md5hex(method + ":" + uri)
	if qop == "" {
		return md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

// verifyDigest validates an Authorization: Digest header. stale is true
// when the credentials were fine but the nonce has expired.
func (a *Authenticator) verifyDigest(r *http.Request, params string) (u store.User, ok, stale bool) {
	p := parseAuthParams(params)
	if alg := p["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
		return u, false, false
	}
	if p["realm"] != a.Realm || p["uri"] != r.RequestURI {
		return u, false, false
	}
	valid, expired := a.digest.checkNonce(p["nonce"])
	if !valid {
		return u, false, false
	}
	qop := p["qop"]
	if qop != "" && qop != "auth" {
		return u, false, false
	}
	user, err := a.Store.UserByName(p["username"])
	if err != nil || user.DigestHA1 == "" {
		return u, false, false
	}
	want := DigestResponse(user.DigestHA1, r.Method, p["uri"], p["nonce"], p["nc"], p["cnonce"], qop)
	if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(p["response"]))) != 1 {
		return u, false, false
	}
	if expired {
		return u, false, true
	}
	if qop != "" {
		nc, err := strconv.ParseUint(p["nc"], 16, 64)
		if err != nil || !a.digest.useNonceCount(p["nonce"], nc) {
			return u, false, false
		}
	}
	return user, true, false
}

func (a *Authenticator) challenge(w http.ResponseWriter, stale bool) {
	s := ""
	if stale {
		s = ", stale=true"
	}
	h := w.Header()
	h.Add("WWW-Authenticate", fmt.Sprintf(`Digest realm=%q, qop="auth", algorithm=MD5, nonce=%q%s`, a.Realm, a.digest.newNonce(), s))
	h.Add("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q, charset="UTF-8"`, a.Realm))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

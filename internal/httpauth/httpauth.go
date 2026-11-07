// Package httpauth verifies HTTP Basic and Digest credentials against the configured users.
package httpauth

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
)

const (
	Realm    = "onvif-cam-sim"
	nonceTTL = 10 * time.Minute
)

// nonces issued by Challenge. A Digest response is accepted only with a nonce we
// issued, within nonceTTL, and with a nonce count higher than the last one seen,
// so a captured Authorization header cannot be replayed.
var nonces = struct {
	sync.Mutex
	m map[string]*nonceState
}{m: map[string]*nonceState{}}

type nonceState struct {
	issued time.Time
	lastNC uint64
}

// Authorized verifies an HTTP Basic or Digest Authorization header.
func Authorized(r *http.Request, users []config.User) bool {
	h := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(h, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
		if err != nil {
			return false
		}
		user, pass, _ := strings.Cut(string(raw), ":")
		for _, u := range users {
			if u.Name == user && subtle.ConstantTimeCompare([]byte(u.Password), []byte(pass)) == 1 {
				return true
			}
		}
	case strings.HasPrefix(h, "Digest "):
		p := parseDigest(strings.TrimPrefix(h, "Digest "))
		if !uriMatches(p["uri"], r) {
			return false
		}
		for _, u := range users {
			if u.Name != p["username"] {
				continue
			}
			ha1 := md5hex(u.Name + ":" + p["realm"] + ":" + u.Password)
			ha2 := md5hex(r.Method + ":" + p["uri"])
			var want string
			if p["qop"] == "auth" {
				want = md5hex(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
			} else {
				want = md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
			}
			if subtle.ConstantTimeCompare([]byte(want), []byte(p["response"])) != 1 {
				return false
			}
			return consumeNonce(p["nonce"], p["nc"])
		}
	}
	return false
}

// uriMatches accepts the digest uri when it equals the request target (path and
// query) or the absolute form of it, as some clients send.
func uriMatches(uri string, r *http.Request) bool {
	if uri == "" {
		return false
	}
	target := r.URL.RequestURI()
	return uri == target || strings.HasSuffix(uri, "://"+r.Host+target)
}

// consumeNonce checks the nonce was issued by us, is fresh, and that nc increases.
func consumeNonce(nonce, ncHex string) bool {
	nonces.Lock()
	defer nonces.Unlock()
	now := time.Now()
	for k, st := range nonces.m {
		if now.Sub(st.issued) > nonceTTL {
			delete(nonces.m, k)
		}
	}
	st, ok := nonces.m[nonce]
	if !ok {
		return false
	}
	if ncHex == "" { // qop-less legacy digest: single use
		delete(nonces.m, nonce)
		return true
	}
	nc, err := strconv.ParseUint(ncHex, 16, 64)
	if err != nil || nc <= st.lastNC {
		return false
	}
	st.lastNC = nc
	return true
}

// Challenge sends fresh Digest and Basic challenges. stale tells a Digest client
// that its credentials may be right but the nonce is no longer accepted.
func Challenge(w http.ResponseWriter) { challenge(w, false) }

// ChallengeStale is used when a Digest header was present but rejected, so a
// client with an expired nonce retries instead of asking the user for a password.
func ChallengeStale(w http.ResponseWriter) { challenge(w, true) }

func challenge(w http.ResponseWriter, stale bool) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	n := hex.EncodeToString(b[:])
	nonces.Lock()
	nonces.m[n] = &nonceState{issued: time.Now()}
	nonces.Unlock()
	extra := ""
	if stale {
		extra = ", stale=true"
	}
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", nonce="%s", qop="auth", algorithm=MD5%s`, Realm, n, extra))
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, Realm))
}

// HasDigest reports whether the request carried a Digest Authorization header.
func HasDigest(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Digest ")
}

// SameOrigin rejects cross-site browser requests to state-changing endpoints.
// Browsers attach cached Basic/Digest credentials to cross-site form posts, so
// without this a page on another site could add or remove cameras (CSRF).
// Non-browser clients send neither header and pass.
func SameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		host := strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
		return strings.EqualFold(host, r.Host)
	}
	return true
}

func parseDigest(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitDigest(s) {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			out[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	return out
}

// splitDigest splits on commas outside quotes.
func splitDigest(s string) []string {
	var parts []string
	inQ := false
	start := 0
	for i, c := range s {
		switch {
		case c == '"':
			inQ = !inQ
		case c == ',' && !inQ:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

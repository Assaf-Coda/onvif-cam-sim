package onvif

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/assafhalevy/onvif-cam-sim/internal/config"
)

const realm = "onvif-cam-sim"

// httpAuthorized verifies an HTTP Basic or Digest Authorization header.
// ponytail: the client's nonce is trusted as-is (no nonce tracking), enough for a simulator.
func httpAuthorized(r *http.Request, users []config.User) bool {
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
			return subtle.ConstantTimeCompare([]byte(want), []byte(p["response"])) == 1
		}
	}
	return false
}

func challenge(w http.ResponseWriter) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", nonce="%s", qop="auth", algorithm=MD5`, realm, hex.EncodeToString(b[:])))
	w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
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

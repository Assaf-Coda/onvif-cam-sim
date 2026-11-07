package httpauth

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
)

var users = []config.User{{Name: "admin", Password: "secret"}}

func digestHeader(nonce, nc, method, uri string) string {
	h := func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }
	ha1 := h("admin:" + Realm + ":secret")
	ha2 := h(method + ":" + uri)
	resp := h(ha1 + ":" + nonce + ":" + nc + ":abc:auth:" + ha2)
	return fmt.Sprintf(`Digest username="admin", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="abc", response="%s"`, Realm, nonce, uri, nc, resp)
}

func issueNonce(t *testing.T) string {
	rec := httptest.NewRecorder()
	Challenge(rec)
	m := regexp.MustCompile(`nonce="([0-9a-f]+)"`).FindStringSubmatch(rec.Header().Get("WWW-Authenticate"))
	if m == nil {
		t.Fatal("no nonce in challenge")
	}
	return m[1]
}

func req(method, uri, auth string) *http.Request {
	r := httptest.NewRequest(method, "http://sim"+uri, nil)
	r.Header.Set("Authorization", auth)
	return r
}

func TestDigest(t *testing.T) {
	n := issueNonce(t)
	if !Authorized(req("GET", "/api/cameras", digestHeader(n, "00000001", "GET", "/api/cameras")), users) {
		t.Fatal("valid digest rejected")
	}
	if Authorized(req("GET", "/api/cameras", digestHeader(n, "00000001", "GET", "/api/cameras")), users) {
		t.Fatal("replayed nc accepted")
	}
	if !Authorized(req("GET", "/api/cameras", digestHeader(n, "00000002", "GET", "/api/cameras")), users) {
		t.Fatal("incremented nc rejected")
	}
	if Authorized(req("GET", "/snapshot.jpg", digestHeader(n, "00000003", "GET", "/api/cameras")), users) {
		t.Fatal("digest for another uri accepted")
	}
	if Authorized(req("GET", "/api/cameras", digestHeader("deadbeef", "00000001", "GET", "/api/cameras")), users) {
		t.Fatal("unknown nonce accepted")
	}
}

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "http://sim:8000/api/cameras", nil)
	if !SameOrigin(r) {
		t.Fatal("plain client rejected")
	}
	r.Header.Set("Origin", "http://sim:8000")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if !SameOrigin(r) {
		t.Fatal("same origin rejected")
	}
	r.Header.Set("Origin", "http://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if SameOrigin(r) {
		t.Fatal("cross-site accepted")
	}
}

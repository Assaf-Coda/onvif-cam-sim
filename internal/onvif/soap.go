// Package onvif implements the ONVIF Profile S device-side SOAP services.
package onvif

import (
	"bytes"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
)

// tokenWindow is how far a UsernameToken's Created may differ from our clock;
// nonces are remembered for the same period so a captured token cannot be replayed.
const tokenWindow = 10 * time.Minute

var seenNonces = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// freshNonce records nonce and reports whether it was unseen within tokenWindow.
func freshNonce(nonce string, now time.Time) bool {
	seenNonces.Lock()
	defer seenNonces.Unlock()
	for k, t := range seenNonces.m {
		if now.Sub(t) > tokenWindow {
			delete(seenNonces.m, k)
		}
	}
	if _, dup := seenNonces.m[nonce]; dup {
		return false
	}
	seenNonces.m[nonce] = now
	return true
}

// parseCreated accepts the xsd:dateTime forms ONVIF clients send.
func parseCreated(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

const (
	nsDevice    = "http://www.onvif.org/ver10/device/wsdl"
	nsMedia     = "http://www.onvif.org/ver10/media/wsdl"
	nsEvents    = "http://www.onvif.org/ver10/events/wsdl"
	nsAnalytics = "http://www.onvif.org/ver20/analytics/wsdl"
)

// request is a parsed SOAP request.
type request struct {
	Action    string // local name of the first Body element
	ActionNS  string
	Body      []byte // inner XML of Body
	MessageID string
	HTTP      *http.Request

	hasToken bool
	username string
	password string // digest or plain text
	pwdType  string
	nonce    string
	created  string
}

type envelope struct {
	Header struct {
		MessageID string `xml:"MessageID"`
		Security  struct {
			UsernameToken struct {
				Username string `xml:"Username"`
				Password struct {
					Type  string `xml:"Type,attr"`
					Value string `xml:",chardata"`
				} `xml:"Password"`
				Nonce   string `xml:"Nonce"`
				Created string `xml:"Created"`
			} `xml:"UsernameToken"`
		} `xml:"Security"`
	} `xml:"Header"`
	Body struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"Body"`
}

func parseRequest(r *http.Request) (*request, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("bad envelope: %w", err)
	}
	req := &request{Body: env.Body.Inner, MessageID: strings.TrimSpace(env.Header.MessageID), HTTP: r}
	ut := env.Header.Security.UsernameToken
	if ut.Username != "" {
		req.hasToken = true
		req.username = ut.Username
		req.password = strings.TrimSpace(ut.Password.Value)
		req.pwdType = ut.Password.Type
		req.nonce = strings.TrimSpace(ut.Nonce)
		req.created = strings.TrimSpace(ut.Created)
	}
	dec := xml.NewDecoder(bytes.NewReader(env.Body.Inner))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("empty body")
		}
		if se, ok := tok.(xml.StartElement); ok {
			req.Action = se.Name.Local
			req.ActionNS = se.Name.Space
			break
		}
	}
	return req, nil
}

// param unmarshals the body into v (a struct with plain element tags).
func (r *request) param(v any) {
	_ = xml.Unmarshal(r.Body, v)
}

// authenticated verifies the WS-UsernameToken against the configured users.
// PasswordDigest tokens must carry a Created within tokenWindow of our clock and
// a nonce not seen before in that window.
func (r *request) authenticated(users []config.User) bool {
	if !r.hasToken {
		return false
	}
	for _, u := range users {
		if u.Name != r.username {
			continue
		}
		if strings.HasSuffix(r.pwdType, "PasswordText") || r.pwdType == "" && r.nonce == "" {
			return subtle.ConstantTimeCompare([]byte(u.Password), []byte(r.password)) == 1
		}
		nonce, err := base64.StdEncoding.DecodeString(r.nonce)
		if err != nil {
			return false
		}
		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(r.created))
		h.Write([]byte(u.Password))
		want := base64.StdEncoding.EncodeToString(h.Sum(nil))
		if subtle.ConstantTimeCompare([]byte(want), []byte(r.password)) != 1 {
			return false
		}
		now := time.Now()
		created, ok := parseCreated(r.created)
		if !ok || created.Sub(now) > tokenWindow || now.Sub(created) > tokenWindow {
			return false
		}
		return freshNonce(r.nonce, now)
	}
	return false
}

const envelopeHead = `<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:wsa="http://www.w3.org/2005/08/addressing" xmlns:wsa5="http://www.w3.org/2005/08/addressing" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:wstop="http://docs.oasis-open.org/wsn/t-1" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tev="http://www.onvif.org/ver10/events/wsdl" xmlns:tan="http://www.onvif.org/ver20/analytics/wsdl" xmlns:tns1="http://www.onvif.org/ver10/topics" xmlns:ter="http://www.onvif.org/ver10/error" xmlns:axt="http://www.onvif.org/ver20/analytics">`

func writeEnvelope(w http.ResponseWriter, status int, relatesTo, header, body string) {
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(status)
	var b strings.Builder
	b.WriteString(envelopeHead)
	if relatesTo != "" || header != "" {
		b.WriteString("<SOAP-ENV:Header>")
		if relatesTo != "" {
			b.WriteString("<wsa:RelatesTo>" + html.EscapeString(relatesTo) + "</wsa:RelatesTo>")
		}
		b.WriteString(header)
		b.WriteString("</SOAP-ENV:Header>")
	}
	b.WriteString("<SOAP-ENV:Body>")
	b.WriteString(body)
	b.WriteString("</SOAP-ENV:Body></SOAP-ENV:Envelope>")
	_, _ = io.WriteString(w, b.String())
}

// soapFault is returned by handlers to produce a SOAP fault.
type soapFault struct {
	status  int    // HTTP status
	code    string // Sender | Receiver
	subcode string // ter:... or wsnt:...
	reason  string
}

func (f *soapFault) Error() string { return f.subcode + ": " + f.reason }

func faultNotAuthorized() *soapFault {
	return &soapFault{400, "Sender", "ter:NotAuthorized", "The action requested requires authorization and the sender is not authorized"}
}

func faultActionNotSupported(action string) *soapFault {
	return &soapFault{400, "Sender", "ter:ActionNotSupported", "Action " + action + " is not supported"}
}

func faultInvalidArg(sub, reason string) *soapFault {
	return &soapFault{400, "Sender", "ter:InvalidArgVal", reason}
}

func writeFault(w http.ResponseWriter, relatesTo string, f *soapFault) {
	body := fmt.Sprintf(`<SOAP-ENV:Fault><SOAP-ENV:Code><SOAP-ENV:Value>SOAP-ENV:%s</SOAP-ENV:Value><SOAP-ENV:Subcode><SOAP-ENV:Value>%s</SOAP-ENV:Value></SOAP-ENV:Subcode></SOAP-ENV:Code><SOAP-ENV:Reason><SOAP-ENV:Text xml:lang="en">%s</SOAP-ENV:Text></SOAP-ENV:Reason></SOAP-ENV:Fault>`,
		f.code, f.subcode, html.EscapeString(f.reason))
	writeEnvelope(w, f.status, relatesTo, "", body)
}

func esc(s string) string { return html.EscapeString(s) }

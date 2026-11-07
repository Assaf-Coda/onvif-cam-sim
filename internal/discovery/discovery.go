// Package discovery answers WS-Discovery probes (UDP 239.255.255.250:3702) with
// one ProbeMatch per camera, and sends Hello on start and Bye on stop.
package discovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"html"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// Device is one advertised endpoint.
type Device struct {
	UUID   string
	XAddr  string
	Scopes []string
}

type Service struct {
	log     *slog.Logger
	mu      sync.Mutex
	devices []Device
	conn    *net.UDPConn
}

var group = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}

func New(log *slog.Logger, devices []Device) *Service {
	return &Service{log: log, devices: devices}
}

// Run listens until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		return fmt.Errorf("ws-discovery listen: %w", err)
	}
	s.mu.Lock()
	s.conn = conn
	_ = conn.SetReadBuffer(1 << 16)
	for _, d := range s.devices {
		s.hello(d)
	}
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		for _, d := range s.devices {
			s.bye(d)
		}
		s.mu.Unlock()
		conn.Close()
	}()
	buf := make([]byte, 1<<16)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.handle(buf[:n], from)
	}
}

type probe struct {
	MessageID string `xml:"Header>MessageID"`
	Types     string `xml:"Body>Probe>Types"`
	Scopes    string `xml:"Body>Probe>Scopes"`
}

func (s *Service) handle(pkt []byte, from *net.UDPAddr) {
	if !bytes.Contains(pkt, []byte("Probe")) {
		return
	}
	var p probe
	if err := xml.Unmarshal(pkt, &p); err != nil || p.MessageID == "" {
		return
	}
	// Types may be empty, dn:NetworkVideoTransmitter or tds:Device; anything else is not for us.
	if t := strings.TrimSpace(p.Types); t != "" && !strings.Contains(t, "NetworkVideoTransmitter") && !strings.Contains(t, "Device") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches", p.MessageID,
			`<d:ProbeMatches>`+s.match(d, "d:ProbeMatch")+`</d:ProbeMatches>`)
		if _, err := s.conn.WriteToUDP([]byte(msg), from); err != nil {
			s.log.Warn("probe match send", "err", err)
		}
	}
	s.log.Debug("ws-discovery probe answered", "from", from, "matches", len(s.devices))
}

// Add announces a device added at runtime with a Hello.
func (s *Service) Add(d Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = append(s.devices, d)
	if s.conn != nil {
		s.hello(d)
	}
}

// Remove sends Bye for the device and stops answering probes for it.
func (s *Service) Remove(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.devices {
		if d.UUID == uuid {
			s.devices = append(s.devices[:i], s.devices[i+1:]...)
			if s.conn != nil {
				s.bye(d)
			}
			return
		}
	}
}

// hello and bye are called with s.mu held and s.conn set.
func (s *Service) hello(d Device) {
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/Hello", "", s.match(d, "d:Hello"))
	_, _ = s.conn.WriteToUDP([]byte(msg), group)
}

func (s *Service) bye(d Device) {
	msg := s.envelope("http://schemas.xmlsoap.org/ws/2005/04/discovery/Bye", "",
		`<d:Bye><wsa:EndpointReference><wsa:Address>urn:uuid:`+d.UUID+`</wsa:Address></wsa:EndpointReference></d:Bye>`)
	_, _ = s.conn.WriteToUDP([]byte(msg), group)
}

func (s *Service) match(d Device, tag string) string {
	return `<` + tag + `><wsa:EndpointReference><wsa:Address>urn:uuid:` + d.UUID + `</wsa:Address></wsa:EndpointReference>` +
		`<d:Types>dn:NetworkVideoTransmitter tds:Device</d:Types>` +
		`<d:Scopes>` + html.EscapeString(strings.Join(d.Scopes, " ")) + `</d:Scopes>` +
		`<d:XAddrs>` + d.XAddr + `</d:XAddrs><d:MetadataVersion>1</d:MetadataVersion></` + tag + `>`
}

func (s *Service) envelope(action, relatesTo, body string) string {
	rel := ""
	if relatesTo != "" {
		rel = `<wsa:RelatesTo>` + html.EscapeString(relatesTo) + `</wsa:RelatesTo>`
	}
	return `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">` +
		`<s:Header><wsa:MessageID>urn:uuid:` + newUUID() + `</wsa:MessageID>` + rel +
		`<wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To><wsa:Action>` + action + `</wsa:Action>` +
		`<d:AppSequence InstanceId="` + fmt.Sprint(instance) + `" MessageNumber="` + fmt.Sprint(nextSeq()) + `"/></s:Header>` +
		`<s:Body>` + body + `</s:Body></s:Envelope>`
}

var (
	instance = time.Now().Unix()
	seq      int64
)

func nextSeq() int64 { seq++; return seq }

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

package onvif

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	topicCellMotion  = "tns1:RuleEngine/CellMotionDetector/Motion"
	topicMotionAlarm = "tns1:VideoSource/MotionAlarm"
	tokVA            = "va_main"
	ruleName         = "MotionRule"
	maxQueue         = 200
	maxSubscriptions = 32
)

type notification struct {
	topic string
	op    string // Initialized | Changed
	at    time.Time
	state bool
}

type subscription struct {
	id       string
	expires  time.Time
	queue    []notification
	signal   chan struct{} // closed and replaced when new messages arrive
	lastPull time.Time
}

type eventBus struct {
	mu     sync.Mutex
	subs   map[string]*subscription
	motion bool
	nextID int
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[string]*subscription{}}
}

// setMotion is called by the detector on state transitions.
func (b *eventBus) setMotion(active bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.motion = active
	now := time.Now().UTC()
	for _, s := range b.subs {
		b.push(s, b.stateMessages("Changed", now)...)
	}
}

func (b *eventBus) stateMessages(op string, now time.Time) []notification {
	return []notification{
		{topic: topicCellMotion, op: op, at: now, state: b.motion},
		{topic: topicMotionAlarm, op: op, at: now, state: b.motion},
	}
}

// push appends to the queue (dropping the oldest when full) and wakes a waiting PullMessages.
func (b *eventBus) push(s *subscription, n ...notification) {
	s.queue = append(s.queue, n...)
	if len(s.queue) > maxQueue {
		s.queue = s.queue[len(s.queue)-maxQueue:]
	}
	close(s.signal)
	s.signal = make(chan struct{})
}

func (b *eventBus) create(ttl time.Duration) *subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(time.Now())
	if len(b.subs) >= maxSubscriptions {
		// drop the least recently pulled subscription
		var victim *subscription
		for _, s := range b.subs {
			if victim == nil || s.lastPull.Before(victim.lastPull) {
				victim = s
			}
		}
		delete(b.subs, victim.id)
	}
	b.nextID++
	s := &subscription{id: strconv.Itoa(b.nextID), expires: time.Now().Add(ttl), signal: make(chan struct{}), lastPull: time.Now()}
	b.push(s, b.stateMessages("Initialized", time.Now().UTC())...)
	b.subs[s.id] = s
	return s
}

func (b *eventBus) get(id string) *subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(time.Now())
	return b.subs[id]
}

func (b *eventBus) remove(id string) {
	b.mu.Lock()
	delete(b.subs, id)
	b.mu.Unlock()
}

func (b *eventBus) sweepLocked(now time.Time) {
	for id, s := range b.subs {
		if now.After(s.expires) {
			delete(b.subs, id)
		}
	}
}

// pull waits up to timeout for messages and returns at most limit of them.
func (b *eventBus) pull(s *subscription, timeout time.Duration, limit int) []notification {
	deadline := time.Now().Add(timeout)
	for {
		b.mu.Lock()
		s.lastPull = time.Now()
		if len(s.queue) > 0 {
			n := min(limit, len(s.queue))
			out := append([]notification{}, s.queue[:n]...)
			s.queue = s.queue[n:]
			b.mu.Unlock()
			return out
		}
		sig := s.signal
		b.mu.Unlock()
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil
		}
		select {
		case <-sig:
		case <-time.After(wait):
			return nil
		}
	}
}

func (b *eventBus) sync(s *subscription) {
	b.mu.Lock()
	b.push(s, b.stateMessages("Initialized", time.Now().UTC())...)
	b.mu.Unlock()
}

// ---- SOAP ----

func (s *Server) subAddress(r *request, id string) string {
	return s.addr(r, "events_service/sub/"+id)
}

// subFromPath extracts the subscription id from /onvif/events_service/sub/<id>.
func subFromPath(p string) string {
	i := strings.LastIndex(p, "/sub/")
	if i < 0 {
		return ""
	}
	return p[i+5:]
}

func xsdTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// parseDuration handles xsd:duration like PT10S, PT1M30S, PT1H, or an absolute xsd:dateTime.
func parseDuration(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return time.Until(t)
	}
	if !strings.HasPrefix(s, "P") {
		return def
	}
	var d time.Duration
	num := ""
	inTime := false
	for _, c := range s[1:] {
		switch {
		case c == 'T':
			inTime = true
		case c >= '0' && c <= '9' || c == '.':
			num += string(c)
		default:
			v, _ := strconv.ParseFloat(num, 64)
			num = ""
			switch {
			case c == 'D':
				d += time.Duration(v * 24 * float64(time.Hour))
			case c == 'H':
				d += time.Duration(v * float64(time.Hour))
			case c == 'M' && inTime:
				d += time.Duration(v * float64(time.Minute))
			case c == 'S':
				d += time.Duration(v * float64(time.Second))
			}
		}
	}
	if d <= 0 {
		return def
	}
	return d
}

func (n notification) xml() string {
	src := fmt.Sprintf(`<tt:SimpleItem Name="VideoSourceConfigurationToken" Value="%s"/><tt:SimpleItem Name="VideoAnalyticsConfigurationToken" Value="%s"/><tt:SimpleItem Name="Rule" Value="%s"/>`, tokVSC, tokVA, ruleName)
	data := fmt.Sprintf(`<tt:SimpleItem Name="IsMotion" Value="%t"/>`, n.state)
	if n.topic == topicMotionAlarm {
		src = fmt.Sprintf(`<tt:SimpleItem Name="Source" Value="%s"/>`, tokVS)
		data = fmt.Sprintf(`<tt:SimpleItem Name="State" Value="%t"/>`, n.state)
	}
	return `<wsnt:NotificationMessage><wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">` + n.topic + `</wsnt:Topic>` +
		`<wsnt:Message><tt:Message UtcTime="` + xsdTime(n.at) + `" PropertyOperation="` + n.op + `"><tt:Source>` + src + `</tt:Source><tt:Data>` + data + `</tt:Data></tt:Message></wsnt:Message></wsnt:NotificationMessage>`
}

func (s *Server) registerEvents() {
	e := nsEvents
	s.on(e, "GetServiceCapabilities", func(_ *request) (string, error) {
		return `<tev:GetServiceCapabilitiesResponse>` + eventsCaps + `</tev:GetServiceCapabilitiesResponse>`, nil
	})
	s.on(e, "GetEventProperties", func(_ *request) (string, error) {
		return `<tev:GetEventPropertiesResponse><tev:TopicNamespaceLocation>http://www.onvif.org/onvif/ver10/topics/topicns.xml</tev:TopicNamespaceLocation>` +
			`<wsnt:FixedTopicSet>true</wsnt:FixedTopicSet><wstop:TopicSet>` +
			`<tns1:RuleEngine><CellMotionDetector><Motion wstop:topic="true"><tt:MessageDescription IsProperty="true"><tt:Source><tt:SimpleItemDescription Name="VideoSourceConfigurationToken" Type="tt:ReferenceToken"/><tt:SimpleItemDescription Name="VideoAnalyticsConfigurationToken" Type="tt:ReferenceToken"/><tt:SimpleItemDescription Name="Rule" Type="xsd:string"/></tt:Source><tt:Data><tt:SimpleItemDescription Name="IsMotion" Type="xsd:boolean"/></tt:Data></tt:MessageDescription></Motion></CellMotionDetector></tns1:RuleEngine>` +
			`<tns1:VideoSource><MotionAlarm wstop:topic="true"><tt:MessageDescription IsProperty="true"><tt:Source><tt:SimpleItemDescription Name="Source" Type="tt:ReferenceToken"/></tt:Source><tt:Data><tt:SimpleItemDescription Name="State" Type="xsd:boolean"/></tt:Data></tt:MessageDescription></MotionAlarm></tns1:VideoSource>` +
			`</wstop:TopicSet>` +
			`<wsnt:TopicExpressionDialect>http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet</wsnt:TopicExpressionDialect><wsnt:TopicExpressionDialect>http://docs.oasis-open.org/wsn/t-1/TopicExpression/Concrete</wsnt:TopicExpressionDialect>` +
			`<tev:MessageContentFilterDialect>http://www.onvif.org/ver10/tev/messageContentFilter/ItemFilter</tev:MessageContentFilterDialect>` +
			`<tev:MessageContentSchemaLocation>http://www.onvif.org/onvif/ver10/schema/onvif.xsd</tev:MessageContentSchemaLocation></tev:GetEventPropertiesResponse>`, nil
	})
	s.on(e, "CreatePullPointSubscription", func(r *request) (string, error) {
		var p struct {
			InitialTerminationTime string `xml:"InitialTerminationTime"`
		}
		r.param(&p)
		ttl := parseDuration(p.InitialTerminationTime, time.Minute)
		sub := s.events.create(ttl)
		now := time.Now()
		return `<tev:CreatePullPointSubscriptionResponse><tev:SubscriptionReference><wsa:Address>` + s.subAddress(r, sub.id) + `</wsa:Address></tev:SubscriptionReference>` +
			`<wsnt:CurrentTime>` + xsdTime(now) + `</wsnt:CurrentTime><wsnt:TerminationTime>` + xsdTime(sub.expires) + `</wsnt:TerminationTime></tev:CreatePullPointSubscriptionResponse>`, nil
	})
	s.on(e, "PullMessages", func(r *request) (string, error) {
		sub := s.events.get(subFromPath(r.HTTP.URL.Path))
		if sub == nil {
			return "", &soapFault{400, "Sender", "wsrf-rw:ResourceUnknownFault", "Subscription not found"}
		}
		var p struct {
			Timeout      string `xml:"Timeout"`
			MessageLimit int    `xml:"MessageLimit"`
		}
		r.param(&p)
		timeout := parseDuration(p.Timeout, 10*time.Second)
		if timeout > 60*time.Second {
			timeout = 60 * time.Second
		}
		if p.MessageLimit <= 0 {
			p.MessageLimit = 100
		}
		msgs := s.events.pull(sub, timeout, p.MessageLimit)
		var b strings.Builder
		b.WriteString(`<tev:PullMessagesResponse><tev:CurrentTime>` + xsdTime(time.Now()) + `</tev:CurrentTime><tev:TerminationTime>` + xsdTime(sub.expires) + `</tev:TerminationTime>`)
		for _, m := range msgs {
			b.WriteString(m.xml())
		}
		b.WriteString(`</tev:PullMessagesResponse>`)
		return b.String(), nil
	})
	s.on(e, "SetSynchronizationPoint", func(r *request) (string, error) {
		if sub := s.events.get(subFromPath(r.HTTP.URL.Path)); sub != nil {
			s.events.sync(sub)
		}
		return `<tev:SetSynchronizationPointResponse/>`, nil
	})
	// Renew and Unsubscribe belong to the WS-BaseNotification SubscriptionManager namespace
	wsnt := "http://docs.oasis-open.org/wsn/b-2"
	s.on(wsnt, "Renew", func(r *request) (string, error) {
		sub := s.events.get(subFromPath(r.HTTP.URL.Path))
		if sub == nil {
			return "", &soapFault{400, "Sender", "wsrf-rw:ResourceUnknownFault", "Subscription not found"}
		}
		var p struct {
			TerminationTime string `xml:"TerminationTime"`
		}
		r.param(&p)
		s.events.mu.Lock()
		sub.expires = time.Now().Add(parseDuration(p.TerminationTime, time.Minute))
		exp := sub.expires
		s.events.mu.Unlock()
		return `<wsnt:RenewResponse><wsnt:TerminationTime>` + xsdTime(exp) + `</wsnt:TerminationTime><wsnt:CurrentTime>` + xsdTime(time.Now()) + `</wsnt:CurrentTime></wsnt:RenewResponse>`, nil
	})
	s.on(wsnt, "Unsubscribe", func(r *request) (string, error) {
		s.events.remove(subFromPath(r.HTTP.URL.Path))
		return `<wsnt:UnsubscribeResponse/>`, nil
	})
}

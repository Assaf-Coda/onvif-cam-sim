package onvif

import (
	"fmt"
	"strings"
	"time"
)

func (s *Server) scopes() []string {
	return []string{
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/type/audio_encoder",
		"onvif://www.onvif.org/Profile/Streaming",
		"onvif://www.onvif.org/name/" + s.cam.Cfg.Name,
		"onvif://www.onvif.org/hardware/" + s.cam.Cfg.Model,
		"onvif://www.onvif.org/location/simulated",
	}
}

// Scopes is used by WS-Discovery.
func (s *Server) Scopes() []string { return s.scopes() }

func (s *Server) mac() string {
	u := strings.ReplaceAll(s.cam.UUID, "-", "")
	return fmt.Sprintf("02:%s:%s:%s:%s:%s", u[0:2], u[2:4], u[4:6], u[6:8], u[8:10])
}

func (s *Server) registerDevice() {
	d := nsDevice
	s.on(d, "GetSystemDateAndTime", func(_ *request) (string, error) {
		now := time.Now().UTC()
		dt := func(t time.Time) string {
			return fmt.Sprintf(`<tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date>`,
				t.Hour(), t.Minute(), t.Second(), t.Year(), int(t.Month()), t.Day())
		}
		return `<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime><tt:DateTimeType>NTP</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings><tt:TimeZone><tt:TZ>UTC0</tt:TZ></tt:TimeZone><tt:UTCDateTime>` + dt(now) + `</tt:UTCDateTime><tt:LocalDateTime>` + dt(now) + `</tt:LocalDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`, nil
	})
	s.on(d, "SetSystemDateAndTime", func(_ *request) (string, error) {
		return `<tds:SetSystemDateAndTimeResponse/>`, nil
	})
	s.on(d, "GetDeviceInformation", func(_ *request) (string, error) {
		c := s.cam.Cfg
		return fmt.Sprintf(`<tds:GetDeviceInformationResponse><tds:Manufacturer>%s</tds:Manufacturer><tds:Model>%s</tds:Model><tds:FirmwareVersion>%s</tds:FirmwareVersion><tds:SerialNumber>%s</tds:SerialNumber><tds:HardwareId>%s</tds:HardwareId></tds:GetDeviceInformationResponse>`,
			esc(c.Manufacturer), esc(c.Model), esc(c.Firmware), esc(c.Serial), esc(c.Model)), nil
	})
	s.on(d, "GetCapabilities", func(r *request) (string, error) {
		return `<tds:GetCapabilitiesResponse><tds:Capabilities>` +
			`<tt:Analytics><tt:XAddr>` + s.addr(r, "analytics_service") + `</tt:XAddr><tt:RuleSupport>true</tt:RuleSupport><tt:AnalyticsModuleSupport>true</tt:AnalyticsModuleSupport></tt:Analytics>` +
			`<tt:Device><tt:XAddr>` + s.addr(r, "device_service") + `</tt:XAddr>` +
			`<tt:Network><tt:IPFilter>false</tt:IPFilter><tt:ZeroConfiguration>false</tt:ZeroConfiguration><tt:IPVersion6>false</tt:IPVersion6><tt:DynDNS>false</tt:DynDNS></tt:Network>` +
			`<tt:System><tt:DiscoveryResolve>false</tt:DiscoveryResolve><tt:DiscoveryBye>true</tt:DiscoveryBye><tt:RemoteDiscovery>false</tt:RemoteDiscovery><tt:SystemBackup>false</tt:SystemBackup><tt:SystemLogging>false</tt:SystemLogging><tt:FirmwareUpgrade>false</tt:FirmwareUpgrade><tt:SupportedVersions><tt:Major>2</tt:Major><tt:Minor>60</tt:Minor></tt:SupportedVersions></tt:System>` +
			`<tt:IO><tt:InputConnectors>0</tt:InputConnectors><tt:RelayOutputs>0</tt:RelayOutputs></tt:IO>` +
			`<tt:Security><tt:TLS1.1>false</tt:TLS1.1><tt:TLS1.2>` + fmt.Sprint(s.httpsPort > 0) + `</tt:TLS1.2><tt:OnboardKeyGeneration>false</tt:OnboardKeyGeneration><tt:AccessPolicyConfig>false</tt:AccessPolicyConfig><tt:X.509Token>false</tt:X.509Token><tt:SAMLToken>false</tt:SAMLToken><tt:KerberosToken>false</tt:KerberosToken><tt:RELToken>false</tt:RELToken></tt:Security></tt:Device>` +
			`<tt:Events><tt:XAddr>` + s.addr(r, "events_service") + `</tt:XAddr><tt:WSSubscriptionPolicySupport>false</tt:WSSubscriptionPolicySupport><tt:WSPullPointSupport>true</tt:WSPullPointSupport><tt:WSPausableSubscriptionManagerInterfaceSupport>false</tt:WSPausableSubscriptionManagerInterfaceSupport></tt:Events>` +
			`<tt:Media><tt:XAddr>` + s.addr(r, "media_service") + `</tt:XAddr><tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>true</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP></tt:StreamingCapabilities><tt:Extension><tt:ProfileCapabilities><tt:MaximumNumberOfProfiles>1</tt:MaximumNumberOfProfiles></tt:ProfileCapabilities></tt:Extension></tt:Media>` +
			`</tds:Capabilities></tds:GetCapabilitiesResponse>`, nil
	})
	s.on(d, "GetServices", func(r *request) (string, error) {
		var p struct {
			IncludeCapability bool `xml:"IncludeCapability"`
		}
		r.param(&p)
		svc := func(ns, path string, major, minor int, capXML string) string {
			out := `<tds:Service><tds:Namespace>` + ns + `</tds:Namespace><tds:XAddr>` + s.addr(r, path) + `</tds:XAddr>`
			if p.IncludeCapability && capXML != "" {
				out += `<tds:Capabilities>` + capXML + `</tds:Capabilities>`
			}
			return out + fmt.Sprintf(`<tds:Version><tt:Major>%d</tt:Major><tt:Minor>%d</tt:Minor></tt:Version></tds:Service>`, major, minor)
		}
		return `<tds:GetServicesResponse>` +
			svc(nsDevice, "device_service", 2, 60, deviceCaps) +
			svc(nsMedia, "media_service", 2, 60, mediaCaps) +
			svc(nsEvents, "events_service", 2, 60, eventsCaps) +
			svc(nsAnalytics, "analytics_service", 2, 60, analyticsCaps) +
			`</tds:GetServicesResponse>`, nil
	})
	s.on(d, "GetServiceCapabilities", func(_ *request) (string, error) {
		return `<tds:GetServiceCapabilitiesResponse>` + deviceCaps + `</tds:GetServiceCapabilitiesResponse>`, nil
	})
	s.on(d, "GetScopes", func(_ *request) (string, error) {
		var b strings.Builder
		b.WriteString(`<tds:GetScopesResponse>`)
		for _, sc := range s.scopes() {
			b.WriteString(`<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>` + esc(sc) + `</tt:ScopeItem></tds:Scopes>`)
		}
		b.WriteString(`</tds:GetScopesResponse>`)
		return b.String(), nil
	})
	s.on(d, "GetHostname", func(_ *request) (string, error) {
		return `<tds:GetHostnameResponse><tds:HostnameInformation><tt:FromDHCP>false</tt:FromDHCP><tt:Name>` + esc(s.cam.Cfg.Name) + `</tt:Name></tds:HostnameInformation></tds:GetHostnameResponse>`, nil
	})
	s.on(d, "GetNetworkInterfaces", func(_ *request) (string, error) {
		return fmt.Sprintf(`<tds:GetNetworkInterfacesResponse><tds:NetworkInterfaces token="eth0"><tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>eth0</tt:Name><tt:HwAddress>%s</tt:HwAddress><tt:MTU>1500</tt:MTU></tt:Info><tt:IPv4><tt:Enabled>true</tt:Enabled><tt:Config><tt:Manual><tt:Address>%s</tt:Address><tt:PrefixLength>24</tt:PrefixLength></tt:Manual><tt:DHCP>false</tt:DHCP></tt:Config></tt:IPv4></tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse>`, s.mac(), s.ip), nil
	})
	s.on(d, "GetNetworkProtocols", func(_ *request) (string, error) {
		return fmt.Sprintf(`<tds:GetNetworkProtocolsResponse><tds:NetworkProtocols><tt:Name>HTTP</tt:Name><tt:Enabled>true</tt:Enabled><tt:Port>%d</tt:Port></tds:NetworkProtocols><tds:NetworkProtocols><tt:Name>RTSP</tt:Name><tt:Enabled>true</tt:Enabled><tt:Port>%d</tt:Port></tds:NetworkProtocols></tds:GetNetworkProtocolsResponse>`, s.port, s.rtspPort), nil
	})
	s.on(d, "GetNetworkDefaultGateway", func(_ *request) (string, error) {
		return `<tds:GetNetworkDefaultGatewayResponse><tds:NetworkGateway><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tds:NetworkGateway></tds:GetNetworkDefaultGatewayResponse>`, nil
	})
	s.on(d, "GetDNS", func(_ *request) (string, error) {
		return `<tds:GetDNSResponse><tds:DNSInformation><tt:FromDHCP>false</tt:FromDHCP></tds:DNSInformation></tds:GetDNSResponse>`, nil
	})
	s.on(d, "GetNTP", func(_ *request) (string, error) {
		return `<tds:GetNTPResponse><tds:NTPInformation><tt:FromDHCP>false</tt:FromDHCP></tds:NTPInformation></tds:GetNTPResponse>`, nil
	})
	s.on(d, "GetDiscoveryMode", func(_ *request) (string, error) {
		return `<tds:GetDiscoveryModeResponse><tds:DiscoveryMode>Discoverable</tds:DiscoveryMode></tds:GetDiscoveryModeResponse>`, nil
	})
	s.on(d, "GetUsers", func(_ *request) (string, error) {
		var b strings.Builder
		b.WriteString(`<tds:GetUsersResponse>`)
		for _, u := range s.users {
			b.WriteString(`<tds:User><tt:Username>` + esc(u.Name) + `</tt:Username><tt:UserLevel>Administrator</tt:UserLevel></tds:User>`)
		}
		b.WriteString(`</tds:GetUsersResponse>`)
		return b.String(), nil
	})
	s.on(d, "GetRelayOutputs", func(_ *request) (string, error) {
		return `<tds:GetRelayOutputsResponse/>`, nil
	})
	s.on(d, "GetWsdlUrl", func(_ *request) (string, error) {
		return `<tds:GetWsdlUrlResponse><tds:WsdlUrl>http://www.onvif.org/</tds:WsdlUrl></tds:GetWsdlUrlResponse>`, nil
	})
	s.on(d, "SystemReboot", func(_ *request) (string, error) {
		return `<tds:SystemRebootResponse><tds:Message>Reboot simulated</tds:Message></tds:SystemRebootResponse>`, nil
	})
	s.on(d, "GetEndpointReference", func(_ *request) (string, error) {
		return `<tds:GetEndpointReferenceResponse><tds:GUID>` + s.cam.UUID + `</tds:GUID></tds:GetEndpointReferenceResponse>`, nil
	})
}

const deviceCaps = `<tds:Capabilities><tds:Network IPFilter="false" ZeroConfiguration="false" IPVersion6="false" DynDNS="false" Dot11Configuration="false" HostnameFromDHCP="false" NTP="0" DHCPv6="false"/><tds:Security TLS1.0="false" TLS1.1="false" TLS1.2="false" OnboardKeyGeneration="false" AccessPolicyConfig="false" DefaultAccessPolicy="false" Dot1X="false" RemoteUserHandling="false" X.509Token="false" SAMLToken="false" KerberosToken="false" UsernameToken="true" HttpDigest="true" RELToken="false"/><tds:System DiscoveryResolve="false" DiscoveryBye="true" RemoteDiscovery="false" SystemBackup="false" SystemLogging="false" FirmwareUpgrade="false" HttpFirmwareUpgrade="false" HttpSystemBackup="false" HttpSystemLogging="false" HttpSupportInformation="false" StorageConfiguration="false"/></tds:Capabilities>`

const mediaCaps = `<trt:Capabilities SnapshotUri="true" Rotation="false" VideoSourceMode="false" OSD="false"><trt:ProfileCapabilities MaximumNumberOfProfiles="1"/><trt:StreamingCapabilities RTPMulticast="false" RTP_TCP="true" RTP_RTSP_TCP="true" NonAggregateControl="false"/></trt:Capabilities>`

const eventsCaps = `<tev:Capabilities WSSubscriptionPolicySupport="false" WSPullPointSupport="true" WSPausableSubscriptionManagerInterfaceSupport="false" MaxNotificationProducers="10" MaxPullPoints="10" PersistentNotificationStorage="false"/>`

const analyticsCaps = `<tan:Capabilities RuleSupport="true" AnalyticsModuleSupport="true" CellBasedSceneDescriptionSupported="false" RuleOptionsSupported="false" AnalyticsModuleOptionsSupported="false"/>`

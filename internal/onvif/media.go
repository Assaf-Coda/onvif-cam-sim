package onvif

import (
	"fmt"
	"strings"
)

const (
	tokProfile = "prof_main"
	tokVS      = "vs_main"
	tokVSC     = "vsc_main"
	tokVEC     = "vec_main"
	tokAS      = "as_main"
	tokASC     = "asc_main"
	tokAEC     = "aec_main"
)

func (s *Server) encoding() string {
	if s.cam.Info().VideoCodec == "hevc" {
		return "H265" // not a Media1 enum value; Media2/Profile T is the proper home for H.265
	}
	return "H264"
}

func (s *Server) videoSourceConfig() string {
	i := s.cam.Info()
	return fmt.Sprintf(`<tt:VideoSourceConfiguration token="%s"><tt:Name>VideoSourceConfig</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration>`,
		tokVSC, tokVS, i.Width, i.Height)
}

func (s *Server) videoEncoderConfig() string {
	i := s.cam.Info()
	codec := ""
	if s.encoding() == "H264" {
		codec = `<tt:H264><tt:GovLength>50</tt:GovLength><tt:H264Profile>High</tt:H264Profile></tt:H264>`
	}
	return fmt.Sprintf(`<tt:VideoEncoderConfiguration token="%s"><tt:Name>VideoEncoderConfig</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>5</tt:Quality><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl>%s<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>0</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT60S</tt:SessionTimeout></tt:VideoEncoderConfiguration>`,
		tokVEC, s.encoding(), i.Width, i.Height, int(i.FPS+0.5), codec)
}

func (s *Server) audioSourceConfig() string {
	if !s.cam.Info().HasAudio {
		return ""
	}
	return fmt.Sprintf(`<tt:AudioSourceConfiguration token="%s"><tt:Name>AudioSourceConfig</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken></tt:AudioSourceConfiguration>`, tokASC, tokAS)
}

func (s *Server) audioEncoderConfig() string {
	i := s.cam.Info()
	if !i.HasAudio {
		return ""
	}
	enc, bitrate := "AAC", 96
	switch i.AudioCodec {
	case "pcm_mulaw", "pcm_alaw":
		enc, bitrate = "G711", 64
	}
	sr := i.SampleRate / 1000
	if sr == 0 {
		sr = 48
	}
	return fmt.Sprintf(`<tt:AudioEncoderConfiguration token="%s"><tt:Name>AudioEncoderConfig</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Bitrate>%d</tt:Bitrate><tt:SampleRate>%d</tt:SampleRate><tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>0</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT60S</tt:SessionTimeout></tt:AudioEncoderConfiguration>`,
		tokAEC, enc, bitrate, sr)
}

// extraProfileXML is filled by the analytics service (VideoAnalyticsConfiguration).
func (s *Server) profile(tag string) string {
	return `<` + tag + ` token="` + tokProfile + `" fixed="true"><tt:Name>MainStream</tt:Name>` +
		s.videoSourceConfig() + s.audioSourceConfig() + s.videoEncoderConfig() + s.audioEncoderConfig() +
		s.analyticsConfig() + `</` + tag + `>`
}

func (s *Server) streamURI() string {
	return fmt.Sprintf("rtsp://%s:%d/%s", s.ip, s.rtspPort, s.rtspPath)
}

func (s *Server) registerMedia() {
	m := nsMedia
	checkProfile := func(r *request) error {
		var p struct {
			ProfileToken string `xml:"ProfileToken"`
		}
		r.param(&p)
		if p.ProfileToken != "" && p.ProfileToken != tokProfile {
			return &soapFault{400, "Sender", "ter:InvalidArgVal ter:NoProfile", "The requested profile token does not exist"}
		}
		return nil
	}
	s.on(m, "GetServiceCapabilities", func(_ *request) (string, error) {
		return `<trt:GetServiceCapabilitiesResponse>` + mediaCaps + `</trt:GetServiceCapabilitiesResponse>`, nil
	})
	s.on(m, "GetProfiles", func(_ *request) (string, error) {
		return `<trt:GetProfilesResponse>` + s.profile("trt:Profiles") + `</trt:GetProfilesResponse>`, nil
	})
	s.on(m, "GetProfile", func(r *request) (string, error) {
		if err := checkProfile(r); err != nil {
			return "", err
		}
		return `<trt:GetProfileResponse>` + s.profile("trt:Profile") + `</trt:GetProfileResponse>`, nil
	})
	s.on(m, "GetVideoSources", func(_ *request) (string, error) {
		i := s.cam.Info()
		return fmt.Sprintf(`<trt:GetVideoSourcesResponse><trt:VideoSources token="%s"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources></trt:GetVideoSourcesResponse>`,
			tokVS, int(i.FPS+0.5), i.Width, i.Height), nil
	})
	s.on(m, "GetVideoSourceConfigurations", func(_ *request) (string, error) {
		return `<trt:GetVideoSourceConfigurationsResponse>` + strings.Replace(s.videoSourceConfig(), "tt:VideoSourceConfiguration", "trt:Configurations", 2) + `</trt:GetVideoSourceConfigurationsResponse>`, nil
	})
	s.on(m, "GetVideoSourceConfiguration", func(_ *request) (string, error) {
		return `<trt:GetVideoSourceConfigurationResponse>` + strings.Replace(s.videoSourceConfig(), "tt:VideoSourceConfiguration", "trt:Configuration", 2) + `</trt:GetVideoSourceConfigurationResponse>`, nil
	})
	s.on(m, "GetCompatibleVideoSourceConfigurations", func(_ *request) (string, error) {
		return `<trt:GetCompatibleVideoSourceConfigurationsResponse>` + strings.Replace(s.videoSourceConfig(), "tt:VideoSourceConfiguration", "trt:Configurations", 2) + `</trt:GetCompatibleVideoSourceConfigurationsResponse>`, nil
	})
	s.on(m, "GetVideoEncoderConfigurations", func(_ *request) (string, error) {
		return `<trt:GetVideoEncoderConfigurationsResponse>` + strings.Replace(s.videoEncoderConfig(), "tt:VideoEncoderConfiguration", "trt:Configurations", 2) + `</trt:GetVideoEncoderConfigurationsResponse>`, nil
	})
	s.on(m, "GetVideoEncoderConfiguration", func(_ *request) (string, error) {
		return `<trt:GetVideoEncoderConfigurationResponse>` + strings.Replace(s.videoEncoderConfig(), "tt:VideoEncoderConfiguration", "trt:Configuration", 2) + `</trt:GetVideoEncoderConfigurationResponse>`, nil
	})
	s.on(m, "GetCompatibleVideoEncoderConfigurations", func(_ *request) (string, error) {
		return `<trt:GetCompatibleVideoEncoderConfigurationsResponse>` + strings.Replace(s.videoEncoderConfig(), "tt:VideoEncoderConfiguration", "trt:Configurations", 2) + `</trt:GetCompatibleVideoEncoderConfigurationsResponse>`, nil
	})
	s.on(m, "GetVideoEncoderConfigurationOptions", func(_ *request) (string, error) {
		i := s.cam.Info()
		fps := int(i.FPS + 0.5)
		res := fmt.Sprintf(`<tt:ResolutionsAvailable><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:ResolutionsAvailable>`, i.Width, i.Height)
		codec := fmt.Sprintf(`<tt:H264>%s<tt:GovLengthRange><tt:Min>1</tt:Min><tt:Max>250</tt:Max></tt:GovLengthRange><tt:FrameRateRange><tt:Min>1</tt:Min><tt:Max>%d</tt:Max></tt:FrameRateRange><tt:EncodingIntervalRange><tt:Min>1</tt:Min><tt:Max>1</tt:Max></tt:EncodingIntervalRange><tt:H264ProfilesSupported>High</tt:H264ProfilesSupported></tt:H264>`, res, fps)
		return `<trt:GetVideoEncoderConfigurationOptionsResponse><trt:Options><tt:QualityRange><tt:Min>1</tt:Min><tt:Max>5</tt:Max></tt:QualityRange>` + codec + `</trt:Options></trt:GetVideoEncoderConfigurationOptionsResponse>`, nil
	})
	s.on(m, "SetVideoEncoderConfiguration", func(_ *request) (string, error) {
		return `<trt:SetVideoEncoderConfigurationResponse/>`, nil
	})
	s.on(m, "GetGuaranteedNumberOfVideoEncoderInstances", func(_ *request) (string, error) {
		return `<trt:GetGuaranteedNumberOfVideoEncoderInstancesResponse><trt:TotalNumber>1</trt:TotalNumber><trt:H264>1</trt:H264></trt:GetGuaranteedNumberOfVideoEncoderInstancesResponse>`, nil
	})
	s.on(m, "GetAudioSources", func(_ *request) (string, error) {
		if !s.cam.Info().HasAudio {
			return `<trt:GetAudioSourcesResponse/>`, nil
		}
		return fmt.Sprintf(`<trt:GetAudioSourcesResponse><trt:AudioSources token="%s"><tt:Channels>%d</tt:Channels></trt:AudioSources></trt:GetAudioSourcesResponse>`, tokAS, max(1, s.cam.Info().Channels)), nil
	})
	s.on(m, "GetAudioSourceConfigurations", func(_ *request) (string, error) {
		return `<trt:GetAudioSourceConfigurationsResponse>` + strings.Replace(s.audioSourceConfig(), "tt:AudioSourceConfiguration", "trt:Configurations", 2) + `</trt:GetAudioSourceConfigurationsResponse>`, nil
	})
	s.on(m, "GetCompatibleAudioSourceConfigurations", func(_ *request) (string, error) {
		return `<trt:GetCompatibleAudioSourceConfigurationsResponse>` + strings.Replace(s.audioSourceConfig(), "tt:AudioSourceConfiguration", "trt:Configurations", 2) + `</trt:GetCompatibleAudioSourceConfigurationsResponse>`, nil
	})
	s.on(m, "GetAudioEncoderConfigurations", func(_ *request) (string, error) {
		return `<trt:GetAudioEncoderConfigurationsResponse>` + strings.Replace(s.audioEncoderConfig(), "tt:AudioEncoderConfiguration", "trt:Configurations", 2) + `</trt:GetAudioEncoderConfigurationsResponse>`, nil
	})
	s.on(m, "GetAudioEncoderConfiguration", func(_ *request) (string, error) {
		return `<trt:GetAudioEncoderConfigurationResponse>` + strings.Replace(s.audioEncoderConfig(), "tt:AudioEncoderConfiguration", "trt:Configuration", 2) + `</trt:GetAudioEncoderConfigurationResponse>`, nil
	})
	s.on(m, "GetCompatibleAudioEncoderConfigurations", func(_ *request) (string, error) {
		return `<trt:GetCompatibleAudioEncoderConfigurationsResponse>` + strings.Replace(s.audioEncoderConfig(), "tt:AudioEncoderConfiguration", "trt:Configurations", 2) + `</trt:GetCompatibleAudioEncoderConfigurationsResponse>`, nil
	})
	s.on(m, "GetAudioEncoderConfigurationOptions", func(_ *request) (string, error) {
		return `<trt:GetAudioEncoderConfigurationOptionsResponse><trt:Options><tt:Options><tt:Encoding>AAC</tt:Encoding><tt:BitrateList><tt:Items>96</tt:Items></tt:BitrateList><tt:SampleRateList><tt:Items>48</tt:Items></tt:SampleRateList></tt:Options></trt:Options></trt:GetAudioEncoderConfigurationOptionsResponse>`, nil
	})
	s.on(m, "GetStreamUri", func(r *request) (string, error) {
		if err := checkProfile(r); err != nil {
			return "", err
		}
		return `<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>` + s.streamURI() + `</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT60S</tt:Timeout></trt:MediaUri></trt:GetStreamUriResponse>`, nil
	})
	s.on(m, "GetSnapshotUri", func(r *request) (string, error) {
		if err := checkProfile(r); err != nil {
			return "", err
		}
		return fmt.Sprintf(`<trt:GetSnapshotUriResponse><trt:MediaUri><tt:Uri>http://%s:%d/snapshot.jpg</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT60S</tt:Timeout></trt:MediaUri></trt:GetSnapshotUriResponse>`, s.ip, s.port), nil
	})
}

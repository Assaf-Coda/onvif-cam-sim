package onvif

import (
	"fmt"
	"strings"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
)

// polygonXML renders a normalized 0..1 polygon in ONVIF's -1..1 coordinate space (y up).
func polygonXML(poly []config.Point) string {
	var b strings.Builder
	b.WriteString(`<tt:Polygon>`)
	for _, p := range poly {
		b.WriteString(fmt.Sprintf(`<tt:Point x="%.4f" y="%.4f"/>`, 2*p[0]-1, 1-2*p[1]))
	}
	b.WriteString(`</tt:Polygon>`)
	return b.String()
}

func (s *Server) rulesXML() string {
	m := s.cam.MotionCfg()
	var b strings.Builder
	// the cell motion rule carries the sensitivity and hold time; it is what the events refer to
	b.WriteString(fmt.Sprintf(`<tt:Rule Name="%s" Type="tt:CellMotionDetector"><tt:Parameters><tt:SimpleItem Name="MinCount" Value="%d"/><tt:SimpleItem Name="AlarmOnDelay" Value="0"/><tt:SimpleItem Name="AlarmOffDelay" Value="%d"/><tt:SimpleItem Name="ActiveCells" Value="0P8A8A=="/></tt:Parameters></tt:Rule>`,
		ruleName, int(m.Sensitivity*1000), int(m.HoldSeconds*1000)))
	// one MotionRegionDetector rule per include zone
	for i, poly := range m.Include {
		b.WriteString(fmt.Sprintf(`<tt:Rule Name="IncludeZone%d" Type="axt:MotionRegionDetector"><tt:Parameters><tt:ElementItem Name="MotionRegion"><axt:MotionRegionConfig Armed="true">%s</axt:MotionRegionConfig></tt:ElementItem></tt:Parameters></tt:Rule>`, i+1, polygonXML(poly)))
	}
	// exclude zones have no ONVIF rule type; exposed disarmed so a client can still see them
	for i, poly := range m.Exclude {
		b.WriteString(fmt.Sprintf(`<tt:Rule Name="ExcludeZone%d" Type="axt:MotionRegionDetector"><tt:Parameters><tt:ElementItem Name="MotionRegion"><axt:MotionRegionConfig Armed="false">%s</axt:MotionRegionConfig></tt:ElementItem></tt:Parameters></tt:Rule>`, i+1, polygonXML(poly)))
	}
	return b.String()
}

func (s *Server) modulesXML() string {
	return fmt.Sprintf(`<tt:AnalyticsModule Name="MotionEngine" Type="tt:CellMotionEngine"><tt:Parameters><tt:SimpleItem Name="Sensitivity" Value="%d"/><tt:ElementItem Name="Layout"><tt:CellLayout Columns="%d" Rows="%d"><tt:Transformation><tt:Translate x="-1.0" y="-1.0"/><tt:Scale x="%.6f" y="%.6f"/></tt:Transformation></tt:CellLayout></tt:ElementItem></tt:Parameters></tt:AnalyticsModule>`,
		int(100-min(100, s.cam.MotionCfg().Sensitivity*1000)), 32, 18, 2.0/32, 2.0/18)
}

// analyticsConfig is the VideoAnalyticsConfiguration embedded in the media profile.
func (s *Server) analyticsConfig() string {
	if !s.cam.MotionCfg().IsEnabled() {
		return ""
	}
	return `<tt:VideoAnalyticsConfiguration token="` + tokVA + `"><tt:Name>MotionAnalytics</tt:Name><tt:UseCount>1</tt:UseCount>` +
		`<tt:AnalyticsEngineConfiguration>` + s.modulesXML() + `</tt:AnalyticsEngineConfiguration>` +
		`<tt:RuleEngineConfiguration>` + s.rulesXML() + `</tt:RuleEngineConfiguration></tt:VideoAnalyticsConfiguration>`
}

func (s *Server) registerAnalytics() {
	a := nsAnalytics
	s.on(a, "GetServiceCapabilities", func(_ *request) (string, error) {
		return `<tan:GetServiceCapabilitiesResponse>` + analyticsCaps + `</tan:GetServiceCapabilitiesResponse>`, nil
	})
	s.on(a, "GetSupportedRules", func(_ *request) (string, error) {
		return `<tan:GetSupportedRulesResponse><tan:SupportedRules><tt:RuleDescription Name="tt:CellMotionDetector"><tt:Parameters><tt:SimpleItemDescription Name="MinCount" Type="xsd:integer"/><tt:SimpleItemDescription Name="AlarmOnDelay" Type="xsd:integer"/><tt:SimpleItemDescription Name="AlarmOffDelay" Type="xsd:integer"/><tt:SimpleItemDescription Name="ActiveCells" Type="xsd:base64Binary"/></tt:Parameters><tt:Messages IsProperty="true"><tt:Source><tt:SimpleItemDescription Name="VideoSourceConfigurationToken" Type="tt:ReferenceToken"/><tt:SimpleItemDescription Name="VideoAnalyticsConfigurationToken" Type="tt:ReferenceToken"/><tt:SimpleItemDescription Name="Rule" Type="xsd:string"/></tt:Source><tt:Data><tt:SimpleItemDescription Name="IsMotion" Type="xsd:boolean"/></tt:Data><tt:ParentTopic>` + topicCellMotion + `</tt:ParentTopic></tt:Messages></tt:RuleDescription>` +
			`<tt:RuleDescription Name="axt:MotionRegionDetector"><tt:Parameters><tt:ElementItemDescription Name="MotionRegion" Type="axt:MotionRegionConfig"/></tt:Parameters></tt:RuleDescription></tan:SupportedRules></tan:GetSupportedRulesResponse>`, nil
	})
	s.on(a, "GetRules", func(_ *request) (string, error) {
		return `<tan:GetRulesResponse>` + strings.ReplaceAll(s.rulesXML(), "tt:Rule ", "tan:Rule ") + `</tan:GetRulesResponse>`, nil
	})
	s.on(a, "GetAnalyticsModules", func(_ *request) (string, error) {
		return `<tan:GetAnalyticsModulesResponse>` + strings.Replace(s.modulesXML(), "tt:AnalyticsModule ", "tan:AnalyticsModule ", 1) + `</tan:GetAnalyticsModulesResponse>`, nil
	})
	s.on(a, "GetSupportedAnalyticsModules", func(_ *request) (string, error) {
		return `<tan:GetSupportedAnalyticsModulesResponse><tan:SupportedAnalyticsModules><tt:AnalyticsModuleDescription Name="tt:CellMotionEngine" maxInstances="1"><tt:Parameters><tt:SimpleItemDescription Name="Sensitivity" Type="xsd:integer"/><tt:ElementItemDescription Name="Layout" Type="tt:CellLayout"/></tt:Parameters></tt:AnalyticsModuleDescription></tan:SupportedAnalyticsModules></tan:GetSupportedAnalyticsModulesResponse>`, nil
	})
	// Media-side lookups of the analytics configuration
	m := nsMedia
	s.on(m, "GetVideoAnalyticsConfigurations", func(_ *request) (string, error) {
		return `<trt:GetVideoAnalyticsConfigurationsResponse>` + strings.Replace(s.analyticsConfig(), "tt:VideoAnalyticsConfiguration", "trt:Configurations", 2) + `</trt:GetVideoAnalyticsConfigurationsResponse>`, nil
	})
	s.on(m, "GetVideoAnalyticsConfiguration", func(_ *request) (string, error) {
		return `<trt:GetVideoAnalyticsConfigurationResponse>` + strings.Replace(s.analyticsConfig(), "tt:VideoAnalyticsConfiguration", "trt:Configuration", 2) + `</trt:GetVideoAnalyticsConfigurationResponse>`, nil
	})
}

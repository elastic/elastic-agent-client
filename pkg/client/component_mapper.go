package client

import (
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func MapComponent(pc *proto.Component) (mapped *Component) {
	if pc == nil {
		return nil
	}

	return &Component{
		Config:           pc,
		Limits:           MapLimits(pc.Limits),
		APM:              MapAPM(pc.ApmConfig),
		GlobalProcessors: MapGlobalProcessors(pc.Processors),
	}
}

func MapLimits(limits *proto.ComponentLimits) *Limits {
	if limits == nil {
		return nil
	}

	return &Limits{maxProcs: limits.GoMaxProcs}
}

func MapGlobalProcessors(pp *proto.GlobalProcessorsConfig) *GlobalProcessorsConfig {
	if pp == nil {
		return nil
	}

	mapped := &GlobalProcessorsConfig{Configs: make(map[string]*ProcessorConfig, len(pp.Configs))}

	for k, v := range pp.Configs {
		mapped.Configs[k] = mapProcessorConfig(v)
	}

	return mapped
}

func mapProcessorConfig(v *proto.ProcessorConfig) *ProcessorConfig {
	if v == nil {
		return nil
	}

	return &ProcessorConfig{
		Enabled: v.Enabled,
		Config:  v.Config.AsMap(),
	}
}

func MapAPM(pa *proto.APMConfig) (mapped *APMConfig) {
	if pa == nil {
		return nil
	}

	return &APMConfig{
		Elastic: MapElasticAPM(pa.Elastic),
	}
}

func MapElasticAPM(eAPM *proto.ElasticAPM) *ElasticAPM {
	if eAPM == nil {
		return nil
	}
	return &ElasticAPM{
		TLS:          MapElasticAPMTLS(eAPM.Tls),
		Environment:  eAPM.Environment,
		APIKey:       eAPM.GetApiKey(),
		SecretToken:  eAPM.GetSecretToken(),
		Hosts:        eAPM.GetHosts(),
		GlobalLabels: eAPM.GetGlobalLabels(),
	}
}

func MapElasticAPMTLS(eTLS *proto.ElasticAPMTLS) *ElasticAPMTLS {
	if eTLS == nil {
		return nil
	}
	return &ElasticAPMTLS{
		SkipVerify: eTLS.GetSkipVerify(),
		ServerCA:   eTLS.GetServerCa(),
		ServerCert: eTLS.GetServerCert(),
	}
}

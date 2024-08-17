package client

import (
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// Component limits
type Limits struct {
	maxProcs uint64
}

// (Elastic) APM configuration
type ElasticAPMTLS struct {
	// Elastic APM TLS config
	SkipVerify bool
	ServerCA   string
	ServerCert string
}

type ElasticAPM struct {
	TLS          *ElasticAPMTLS
	Environment  string
	APIKey       string
	SecretToken  string
	Hosts        []string
	GlobalLabels string
}

// APM configuration
type APMConfig struct {
	Elastic *ElasticAPM
}

// Global processors config

// A processor config. Enabled flag is a separate field to check easily activation/deactivation. All the rest of the config
// is in the generic Struct config field
type ProcessorConfig struct {
	Enabled bool
	Config  map[string]any
}

// Configure some global processors for event data at component level
type GlobalProcessorsConfig struct {
	Configs map[string]*ProcessorConfig
}

type Component struct {
	Config    *proto.Component
	ConfigIdx uint64

	Limits           *Limits
	APM              *APMConfig
	GlobalProcessors *GlobalProcessorsConfig
}

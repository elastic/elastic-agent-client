package client

import (
	"context"
	"fmt"

	"github.com/elastic/elastic-agent-client/v7/pkg/config"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-libs/loader"
	"github.com/elastic/elastic-agent-libs/logp"
)

type mockAgentFile struct {
	Outputs map[string]interface{}   `config:"outputs"`
	Inputs  []map[string]interface{} `config:"inputs"`
}

// newV2Reader generates the reader client for a given config file
func newV2Reader(filename string) (V2, error) {
	cfgLoader := loader.NewLoader(logp.L(), "./")
	rawCfg, err := cfgLoader.Load([]string{filename})
	if err != nil {
		return nil, fmt.Errorf("error loading config file: %w", err)
	}

	var wrapper mockAgentFile
	err = rawCfg.Unpack(&wrapper)
	if err != nil {
		return nil, fmt.Errorf("error unpacking config for unit: %w", err)
	}

	if wrapper.Inputs == nil && wrapper.Outputs == nil {
		return nil, fmt.Errorf("no input or output config found")
	}

	// generate the unit files from the raw config

	inputUnits := []*proto.UnitExpectedConfig{}
	if wrapper.Inputs != nil {
		for _, input := range wrapper.Inputs {
			cfg, err := config.ExpectedConfig(input)
			if err != nil {
				return nil, fmt.Errorf("error generating expected config for unit: %w", err)
			}
			inputUnits = append(inputUnits, cfg)
		}
	}

	outputUnits := []*proto.UnitExpectedConfig{}
	if wrapper.Outputs != nil {
		// don't try to figure out if an input should be sent, let the user's config file do that.
		for outName, outCfg := range wrapper.Outputs {
			outMap, ok := outCfg.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("out type %s could not be unmarshalled to a map", outName)
			}
			outMap["original_key_name"] = outName
			// for outputs, we need to iterate over the map of configured output names
			// most of the time, there's only a single "default" key.
			cfg, err := config.ExpectedConfig(outMap)
			if err != nil {
				return nil, fmt.Errorf("error generating expected config for unit: %w", err)
			}
			outputUnits = append(outputUnits, cfg)
		}

	}

	client := &clientV2{
		fileOutputs: outputUnits,
		fileInputs:  inputUnits,
		filemode:    true,
		agentInfo: &AgentInfo{
			ID: "filemode",
		},
		log:       logp.L(),
		kickCh:    make(chan struct{}, 1),
		errCh:     make(chan error),
		unitsCh:   make(chan UnitChanged),
		diagHooks: make(map[string]diagHook),
	}

	return client, nil
}

// a implementation of the Logger interface used by the client while in filemode
type fileLogger struct {
	log logp.Logger
}

func newFilemodeLogger() LogClient {
	return fileLogger{
		log: *logp.L(),
	}
}

// Log sends the log message upstream to the logp logger while in filemode
func (l fileLogger) Log(_ context.Context, message []byte) error {
	l.log.Infof("filemode logger: %s", string(message))
	return nil
}

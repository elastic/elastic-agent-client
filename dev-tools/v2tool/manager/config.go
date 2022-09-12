// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package manager

import (
	"fmt"

	"github.com/elastic/elastic-agent-client/v7/dev-tools/v2tool/rules"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent/pkg/component"
)

// Config represents the config structure for an entire client
type Config struct {
	Inputs InputList           `config:"inputs"`
	Rules  map[string]RulesCfg `config:"rules"`
	Args   []string            `config:"args"`
}

// RulesCfg represents the rules present in a config
// A custom Unmarhsal function will transform the user YAML into the interface used throughout the tool runtime
type RulesCfg struct {
	Start rules.Rule `config:"start"`
	Stop  rules.Rule `config:"stop"`
}

// InputList is another custom type hack to allow us to correctly unmarshal the unit config
type InputList struct {
	Inputs []*proto.UnitExpectedConfig
}

// UnmarshalYAML wraps the YAML Unmarshal needed by the V2 units config,
// as other components will choke on the data structures used by the default unmarshal behavior
func (cfg *InputList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var temp []map[interface{}]interface{}
	err := unmarshal(&temp)
	if err != nil {
		return fmt.Errorf("error unpacking expected input yaml: %w", err)
	}

	inputs := []*proto.UnitExpectedConfig{}
	for _, input := range temp {
		// more horrible hacks.
		// this yaml marshaller really likes to read things as map[interface{}]interface{},
		// which just breaks everything
		cleaned := cleanUpInterfaceMap(input)
		expectedCfg, err := component.ExpectedConfig(cleaned)
		if err != nil {
			return fmt.Errorf("error generating expected config from map: %w", err)
		}
		inputs = append(inputs, expectedCfg)
	}
	cfg.Inputs = inputs
	return nil
}

// The following are all helper functions to clean up the map[interface{}]interface{} types that the yaml unmarsahller will spit out

func cleanUpInterfaceArray(in []interface{}) []interface{} {
	result := make([]interface{}, len(in))
	for i, v := range in {
		result[i] = cleanUpMapValue(v)
	}
	return result
}

func cleanUpInterfaceMap(in map[interface{}]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range in {
		result[fmt.Sprintf("%v", k)] = cleanUpMapValue(v)
	}
	return result
}

func cleanUpMapValue(v interface{}) interface{} {
	switch v := v.(type) {
	case []interface{}:
		return cleanUpInterfaceArray(v)
	case map[interface{}]interface{}:
		return cleanUpInterfaceMap(v)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

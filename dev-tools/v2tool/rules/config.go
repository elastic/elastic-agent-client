package rules

import (
	"fmt"

	"gopkg.in/yaml.v2"
)

// Rule is a wrapper that contains a check rule
// The interface wrapped in a struct is mostly to deal with
// the weirdness involved in unmarshalling somewhting with an interface
type Rule struct {
	Check Checker
}

// UnmarshalYAML turns the raw yaml value into a Rule struct with a check interface
func (rule *Rule) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// get the raw rule value
	var unpackTo map[string]interface{}
	err := unmarshal(&unpackTo)
	if err != nil {
		return fmt.Errorf("error unpacking rule to raw interface value: %w", err)
	}

	// a given rule can only have one key, for now
	if len(unpackTo) != 1 {
		return fmt.Errorf("Start/stop config can only have one rule, found %d", len(unpackTo))
	}
	keys := make([]string, 0, len(unpackTo))
	for k := range unpackTo {
		keys = append(keys, k)
	}
	ruleKey := keys[0]

	var checkVal Checker
	body := unpackTo[ruleKey]
	switch ruleKey {
	case "OnStart":
		checkVal = &OnStart{}
	case "After":
		checkVal = &After{}
	default:
		return fmt.Errorf("unknown rule of type %s", ruleKey)
	}

	// This is a bit of a hack, and a similar strategy is used by the AST rules unpacker
	// in the elastic-agent, and this API doesn't give us a `rawMessage` fields
	yamlBody, err := yaml.Marshal(body)
	if err != nil {
		return fmt.Errorf("error marshalling body of rule %s back to YAML: %w", ruleKey, err)
	}
	err = yaml.Unmarshal(yamlBody, checkVal)
	rule.Check = checkVal
	return nil
}

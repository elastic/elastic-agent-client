package manager

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/elastic/elastic-agent-client/v7/dev-tools/v2tool/rules"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent/pkg/core/process"
	"gopkg.in/yaml.v2"
)

type InputManager struct {
	logger     *logp.Logger
	client     *process.Info
	Units      []Unit
	clientArgs []string
}

type Rules struct {
	Start rules.Checker
	Stop  rules.Checker
}

type Unit struct {
	Rules RulesCfg
	done  bool
	State *proto.UnitExpected
}

// InputManagerFromCfg creates the input Manager from a path to a yaml config
func InputManagerFromCfg(cfgPath string) (*InputManager, error) {
	fileData, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("error reading config from file %s: %w", cfgPath, err)
	}

	inputMgrCfg := Config{}
	err = yaml.Unmarshal(fileData, &inputMgrCfg)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling yaml for file %s: %w", cfgPath, err)
	}

	// restructure the config into the InputManager
	units := []Unit{}
	for _, input := range inputMgrCfg.Inputs.Inputs {
		ruleCfg, ok := inputMgrCfg.Rules[input.Id]
		if !ok {
			keys := make([]string, 0, len(inputMgrCfg.Rules))
			for k := range inputMgrCfg.Rules {
				keys = append(keys, k)
			}
			return nil, fmt.Errorf("could not find rules for input with ID '%s', available rules are %v", input.Id, keys)
		}

		unit, err := newUnit(input, ruleCfg)
		if err != nil {
			return nil, fmt.Errorf("Error adding unit of ID %s: %w", input.Id, err)
		}
		units = append(units, unit)
	}
	return &InputManager{logger: logp.L(), Units: units, clientArgs: inputMgrCfg.Args}, nil
}

// StartInputProcess starts the V2 client
func (in *InputManager) StartInputProcess(path string) error {
	in.logger.Debugf("Client args are: %v", in.clientArgs)
	proc, err := process.Start(path, os.Geteuid(), os.Getgid(), in.clientArgs, []string{}, attachOutErr)
	if err != nil {
		return fmt.Errorf("error starting process from path %s: %w", path, err)
	}
	in.client = proc
	return nil
}

// WriteToClient writes a byte string (usually the V2 conn info)
func (in *InputManager) WriteToClient(info []byte) error {
	_, err := in.client.Stdin.Write(info)
	if err != nil {
		return fmt.Errorf("failed to write connection information: %w", err)
	} else {
		// if you remove this the V2 client won't get an EOF, be careful
		in.client.Stdin.Close()
	}
	return nil
}

// WaitForClientClose blocks until the client process ends
func (in *InputManager) WaitForClientClose() {
	if in.client == nil {
		return
	}
	waitChan := in.client.Wait()
	for {
		select {
		case end := <-waitChan:
			in.logger.Debugf("Got client stop for pid %d, status %d", end.Pid(), end.ExitCode())
			return
		}
	}
}

func attachOutErr(cmd *exec.Cmd) error {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return nil
}

func newUnit(expected *proto.UnitExpectedConfig, rules RulesCfg) (Unit, error) {
	expectedStart := generateUnitExpected(expected)
	return Unit{State: expectedStart, Rules: rules}, nil
}

// Checkin checkin is called every time the Mock V2 server performs a client checkin.
// It updates the units based on the current state, and returns a new expected config
func (in *InputManager) Checkin(observed *proto.CheckinObserved, started time.Time) *proto.CheckinExpected {
	base := &proto.CheckinExpected{
		AgentInfo: &proto.CheckinAgentInfo{
			Id:       "test-agent",
			Version:  "8.4.0",
			Snapshot: true,
		},
	}

	// Client is starting up, no units are configured yet
	if len(observed.Units) == 0 {
		in.logger.Debugf("No units found, sending starting configuration")
		base.Units = in.startupConfig(started)
	} else {
		// we got some kind of units, update instead
		base.Units = in.configFromObserved(started, observed)
	}

	return base
}

// configFromObserved handles the logic of updating expected units based on our current state
func (in *InputManager) configFromObserved(started time.Time, obs *proto.CheckinObserved) []*proto.UnitExpected {
	// any units not in this array will be removed by the server
	units := []*proto.UnitExpected{}
	for iter, v := range in.Units {
		obsUnit, foundObs := findObserved(v.State.Id, obs.Units)
		// Case, the unit has already been asked to stop
		if v.done {
			if foundObs {
				if obsUnit.State == proto.State_STOPPED { // case: The unit has been marked as done, and now stopped
					in.logger.Debugf("Unit %s marked as done by v2tool, found in state %; removing", v.State.Id, obsUnit.State.String())
					continue
				}
			} else { // case: The unit is done, and has been completely removed
				continue
			}
		}

		if foundObs && obsUnit.State == proto.State_STOPPED {
			in.logger.Warnf("Unit %s was not explicitly stopped, but is now stopped; removing")
			continue
		}
		// unit has previously been sent to client
		if foundObs {
			if obsUnit.State == proto.State_FAILED {
				in.logger.Debugf("Unit %s has been marked as failed; removing", v.State.Id)
				continue
			}
			// Do we no want to stop?
			// The check functions are responsible for checking the actual status of the state
			if !v.done && v.Rules.Stop.Check.Check(started, obsUnit) {
				in.logger.Debugf("Unit %s will be marked as STOPPED", v.State.Id)
				in.Units[iter].State.State = proto.State_STOPPED
				in.Units[iter].State.ConfigStateIdx++
				units = append(units, in.Units[iter].State)
				in.Units[iter].done = true
			} else {
				// unit will continue as normal
				units = append(units, in.Units[iter].State)
			}
		} else {
			// unit doesn't exist, do we want to start?
			if v.Rules.Start.Check.Check(started, nil) {
				in.logger.Debugf("Unit %s will be marked as STARTING")
				units = append(units, in.Units[iter].State)
			}
		}
	}
	return units
}

// returns the unit config for a V2 client's first startup
func (in *InputManager) startupConfig(started time.Time) []*proto.UnitExpected {
	units := []*proto.UnitExpected{}
	for iter, v := range in.Units {
		if !v.Rules.Start.Check.Check(started, nil) {
			continue
		}
		in.logger.Debugf("Unit with ID %s will run at startup", in.Units[iter].State.Id)
		// assume the starting input config is fine
		units = append(units, in.Units[iter].State)
	}
	return units
}

// helper to return an observed unit state based on an ID
func findObserved(id string, units []*proto.UnitObserved) (*proto.UnitObserved, bool) {
	for _, unit := range units {
		if unit.Id == id {
			return unit, true
		}
	}
	return nil, false
}

// helper to turn a unit config into an entire UnitExpected struct used by the MockV2 server
func generateUnitExpected(cfg *proto.UnitExpectedConfig) *proto.UnitExpected {
	return &proto.UnitExpected{
		Id:             cfg.Id,
		Type:           proto.UnitType_INPUT,
		ConfigStateIdx: 0,
		Config:         cfg,
		State:          proto.State_HEALTHY,
		LogLevel:       proto.UnitLogLevel_DEBUG,
	}
}

// helper to form an entire CheckinExpected structure
func createUnitsWithState(state proto.State, input *proto.UnitExpectedConfig, inID string, stateIndex uint64) *proto.CheckinExpected {
	return &proto.CheckinExpected{
		AgentInfo: &proto.CheckinAgentInfo{
			Id:       "test-agent",
			Version:  "8.4.0",
			Snapshot: true,
		},
		Units: []*proto.UnitExpected{
			{
				Id:             inID,
				Type:           proto.UnitType_INPUT,
				ConfigStateIdx: stateIndex,
				Config:         input,
				State:          state,
				LogLevel:       proto.UnitLogLevel_DEBUG,
			},
		},
	}
}

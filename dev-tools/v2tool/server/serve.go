package server

import (
	"flag"
	"fmt"

	"github.com/elastic/elastic-agent-client/v7/dev-tools/v2tool/manager"
	"github.com/elastic/elastic-agent-libs/logp"
)

const (
	defaultConfigName = "./v2-config.yml"
)

var (
	configPath        string
	configFilePath    string
	defaultDebugLevel = false
)

func init() {
	fs := flag.CommandLine
	fs.StringVar(&configFilePath, "c", defaultConfigName, "Configuration file, relative to path.config")
	fs.BoolVar(&defaultDebugLevel, "d", false, "enable debug-level logging")
}

func setupLogger() error {
	logConfig := logp.DefaultConfig(logp.DefaultEnvironment)
	logConfig.Beat = "v2-tool"
	logConfig.ToStderr = true
	logConfig.ToFiles = false
	if defaultDebugLevel == true {
		logConfig.Level = logp.DebugLevel
	} else {
		logConfig.Level = logp.InfoLevel
	}

	err := logp.Configure(logConfig)
	if err != nil {
		return fmt.Errorf("could not initialise logger: %w", err)
	}
	return nil
}

// Run starts and runs the v2tool
func Run(clientPath string) error {
	err := setupLogger()
	if err != nil {
		return fmt.Errorf("error in logger setup: %w", err)
	}
	log := logp.L()

	// Generate a config object for the tool and user unit config
	manager, err := manager.InputManagerFromCfg(configFilePath)

	// Start the supplied V2 client process
	err = manager.StartInputProcess(clientPath)
	if err != nil {
		return fmt.Errorf("error starting client: %s", err)
	}

	log.Debugf("Started client process")
	// start a server instance
	srv, err := NewToolServer(manager)
	if err != nil {
		return fmt.Errorf("error creating v2tool server: %s", err)
	}

	// start the V2 server instance
	err = srv.StartServer()
	if err != nil {
		return fmt.Errorf("error starting v2tool server: %s", err)
	}

	manager.WaitForClientClose()
	return nil
}

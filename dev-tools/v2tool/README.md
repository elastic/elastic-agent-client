# V2Tool

`v2tool` is a simple CLI tool to help develop clients for the `elastic-agent` V2 API. The tool acts like a mock V2 server,
configuring, starting, and stopping the client based on user config.

In it's current state, `v2tool` is fairly simple:

```
Usage:
  v2tool -d -c ./input.yml [CLIENT_EXEC] [flags]

Flags:
  -c, --c string   Configuration file, relative to path.config (default "./v2-config.yml")
  -d, --d          enable debug-level logging
  -h, --help       help for v2tool
```

## Config and rules

To use it, create a `v2-config.yml` file that the `v2tool` will use to configure and start the client.
The config must have an `args`, `rules` and `inputs` section:
```
args:
  - loadgenerator # The list of CLI args that will be passed to the V2 client application
rules: # The rules section determines how an individual input will stop and start
  input/loadgenerator: # This key corrisponds to the ID of a given input in the section below
    start: # Rule for when the tool will start the input unit
      OnStart: {} # Unit will start once the V2 client first reports
    stop: # Rule for when the unit stops
      After:
        time: 10s # The unit will shutdown after 10 seconds
inputs: # Inputs is the config for the entire application. This section can be copy-and-pased from any other input config source.
  - id: input/loadgenerator
    name: test-integration-1
    type: input/loadgenerator
    streams:
      - ...
```

There are currently two rule types that can be used to start and stop a unit:

- `OnStart`: Starts a unit when the client application first checks in.
- `After`: Starts or stops a unit after a given interval of time


## Usage

After the `v2-config.yml` file is in place, just run `v2tool` with a path to the V2 client executable. 
`v2tool` will look for the `yml` file in the current directory:

```
./v2tool -d ../elastic-agent-inputs/elastic-agent-inputs
```

## TODO

- Deal with the `ca.go` library that we stole from `elastic-agent/internal/pkg/core/authority`
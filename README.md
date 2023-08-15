# Elastic Agent Client
This repository implements the control protocol client for the [Elastic Agent](https://github.com/elastic/elastic-agent).
All processes implementing Elastic Agent inputs must implement a control protocol client.

The control protocol is implemented as gRPC service on localhost using port [6789](https://github.com/cmacknz/elastic-agent/blob/67313b282156f56010ea9ee236b3291cb1fea5ff/elastic-agent.yml#L167-L168) by default and secured using mTLS.

This repository currently contains three separate protobuf files:

* [elatic-agent-client.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client.proto) which implements the current
version of the protocol, usually referred to as V2. This version was introduced in Elastic Agent 8.6.0.
* [elastic-agent-client-deprecated.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client-deprecated.proto) which documents
the deprecated V1 protocol that was used until 8.6.0.
* [elastic-agent-client-future.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client-future.proto) documents future extensions
of the V2 protocol that were reviewed as part of the original rewrite from V1 to V2 but never implemented.

The entrypoint for implementing a control protocol client in Go is to create an instance of the client by calling [client.NewV2()](https://github.com/elastic/elastic-agent-client/blob/c699c976fa3092435985dd633c1ed7807a753e74/pkg/client/client_v2.go#L224) or [client.NewV2FromReader()](https://github.com/cmacknz/elastic-agent-client/blob/3551199ffd826a0c4535f5890902a10fa329f301/pkg/client/reader.go#L66).

## Developing

The development process is driven by [Mage](https://magefile.org/). Run `mage -l` to see the list of targets.

When editing the the .proto files first run `mage update:all` to run protoc followed by `mage check:all` to run the checks and tests.

When ready to publish a release for consumption by client implementations, create a tag and a new release at https://github.com/elastic/elastic-agent-client/releases.

## Design
The design of the control protocol follows from the Elastic Agent [architecture](https://github.com/elastic/elastic-agent/blob/main/docs/architecture.md) which
defines the component and unit terminology used in the protocol. The connection sequence is as follows:

1. The agent determines that it should start a new component. Today the majority of components run as subprocesses of the agent. In this case the agent
will launch the process and pass the information needed to connect to the agent's gRPC server on Stdin. The `ConnInfo` message type describes the information
that will be passed to each process at start up:

```protobuf
// Connection information sent to the application on startup so it knows how to connect back to the Elastic Agent.
//
// This is normally sent through stdin and should never be sent across a network un-encrypted.
message ConnInfo {
    // GRPC connection address.
    string addr = 1;
    // Server name to use when connecting over TLS.
    string server_name = 2;
    // Token that the application should send as the unique identifier when connecting over the GRPC.
    string token = 3;
    // CA certificate.
    bytes ca_cert = 4;
    // Peer certificate.
    bytes peer_cert = 5;
    // Peer private key.
    bytes peer_key = 6;
    // Allowed services that spawned process can use. (only used in V2)
    repeated ConnInfoServices services = 7;
}
```

The [client.NewV2FromReader()](https://github.com/cmacknz/elastic-agent-client/blob/3551199ffd826a0c4535f5890902a10fa329f301/pkg/client/reader.go#L66) is a
convenience method for directly initializing a client from Stdin at startup.

2. Clients are expected to immediately establish the `CheckinV2` and `Actions` streaming RPC to allow them to receive their expected configuration and any
actions that are required. The operation of these two RPCs is described in more detail directly in the [elatic-agent-client.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client.proto) file.

## Example Message Exchange

The following example messages were captured from a running Elastic Agent with the system integration configured to collect logs but not metrics.
The message exchange occurs between the agent and a new logfile component implemented by Filebeat. The messages were serialized to JSON using
the [protojson.Marshal](https://pkg.go.dev/google.golang.org/protobuf/encoding/protojson#Marshal). Control protocol message logging is currently
not possible in production because the payloads contain complete integration input configurations, which can contain credentials for the
systems being monitored.

1. The `logfile` component first sends the initial `CheckinObserved` message:

```json
{
  "@timestamp": "2023-08-15T19:41:39.987Z",
  "observed": {
    "token": "27e63445-dff2-48a7-927d-3113ab50769b",
    "versionInfo": {
      "name": "beat-v2-client",
      "version": "8.10.0",
      "meta": {
        "build_time": "2023-08-14 23:40:19 +0000 UTC",
        "commit": "ca76790f16b6f36e131cebd64b6b56d009f7a4dd"
      }
    }
  }
}
```

2. The agent responds back with the initial configuration for the component in the `CheckinExpected` message.
   Note that the configurations sent by the agent map exactly to the `logfile` input section of the agent policy
   along with the relevant `output` section.

<details>

```json
{
  "@timestamp": "2023-08-15T19:41:39.987Z",
  "expected": {
    "units": [
      {
        "id": "log-default-logfile-system-8ec9254c-0f1e-4166-965b-549c2818a0b0",
        "state": "HEALTHY",
        "configStateIdx": "1",
        "config": {
          "source": {
            "data_stream": {
              "namespace": "default"
            },
            "id": "logfile-system-8ec9254c-0f1e-4166-965b-549c2818a0b0",
            "meta": {
              "package": {
                "name": "system",
                "version": "1.38.2"
              }
            },
            "name": "system-1",
            "package_policy_id": "8ec9254c-0f1e-4166-965b-549c2818a0b0",
            "policy": {
              "revision": 3
            },
            "revision": 2,
            "streams": [
              {
                "data_stream": {
                  "dataset": "system.auth",
                  "type": "logs"
                },
                "exclude_files": [
                  ".gz$"
                ],
                "id": "logfile-system.auth-8ec9254c-0f1e-4166-965b-549c2818a0b0",
                "ignore_older": "72h",
                "multiline": {
                  "match": "after",
                  "pattern": "^\\s"
                },
                "paths": [
                  "/var/log/auth.log*",
                  "/var/log/secure*"
                ],
                "processors": [
                  {
                    "add_locale": null
                  }
                ],
                "tags": [
                  "system-auth"
                ]
              },
              {
                "data_stream": {
                  "dataset": "system.syslog",
                  "type": "logs"
                },
                "exclude_files": [
                  ".gz$"
                ],
                "id": "logfile-system.syslog-8ec9254c-0f1e-4166-965b-549c2818a0b0",
                "ignore_older": "72h",
                "multiline": {
                  "match": "after",
                  "pattern": "^\\s"
                },
                "paths": [
                  "/var/log/messages*",
                  "/var/log/syslog*",
                  "/var/log/system*"
                ],
                "processors": [
                  {
                    "add_locale": null
                  }
                ]
              }
            ],
            "type": "log"
          },
          "id": "logfile-system-8ec9254c-0f1e-4166-965b-549c2818a0b0",
          "type": "log",
          "name": "system-1",
          "revision": "2",
          "meta": {
            "source": {
              "package": {
                "name": "system",
                "version": "1.38.2"
              }
            },
            "package": {
              "source": {
                "name": "system",
                "version": "1.38.2"
              },
              "name": "system",
              "version": "1.38.2"
            }
          },
          "dataStream": {
            "source": {
              "namespace": "default"
            },
            "namespace": "default"
          },
          "streams": [
            {
              "source": {
                "data_stream": {
                  "dataset": "system.auth",
                  "type": "logs"
                },
                "exclude_files": [
                  ".gz$"
                ],
                "id": "logfile-system.auth-8ec9254c-0f1e-4166-965b-549c2818a0b0",
                "ignore_older": "72h",
                "multiline": {
                  "match": "after",
                  "pattern": "^\\s"
                },
                "paths": [
                  "/var/log/auth.log*",
                  "/var/log/secure*"
                ],
                "processors": [
                  {
                    "add_locale": null
                  }
                ],
                "tags": [
                  "system-auth"
                ]
              },
              "id": "logfile-system.auth-8ec9254c-0f1e-4166-965b-549c2818a0b0",
              "dataStream": {
                "source": {
                  "dataset": "system.auth",
                  "type": "logs"
                },
                "dataset": "system.auth",
                "type": "logs"
              }
            },
            {
              "source": {
                "data_stream": {
                  "dataset": "system.syslog",
                  "type": "logs"
                },
                "exclude_files": [
                  ".gz$"
                ],
                "id": "logfile-system.syslog-8ec9254c-0f1e-4166-965b-549c2818a0b0",
                "ignore_older": "72h",
                "multiline": {
                  "match": "after",
                  "pattern": "^\\s"
                },
                "paths": [
                  "/var/log/messages*",
                  "/var/log/syslog*",
                  "/var/log/system*"
                ],
                "processors": [
                  {
                    "add_locale": null
                  }
                ]
              },
              "id": "logfile-system.syslog-8ec9254c-0f1e-4166-965b-549c2818a0b0",
              "dataStream": {
                "source": {
                  "dataset": "system.syslog",
                  "type": "logs"
                },
                "dataset": "system.syslog",
                "type": "logs"
              }
            }
          ]
        },
        "logLevel": "INFO"
      },
      {
        "id": "log-default",
        "type": "OUTPUT",
        "state": "HEALTHY",
        "configStateIdx": "1",
        "config": {
          "source": {
            "api_key": "fake:api-key",
            "hosts": [
              "https://localhost:9092"
            ],
            "type": "elasticsearch"
          },
          "type": "elasticsearch"
        },
        "logLevel": "INFO"
      }
    ],
    "features": {
      "source": {
        "agent": {
          "features": {
            "fqdn": {
              "enabled": false
            }
          }
        }
      },
      "fqdn": {}
    },
    "featuresIdx": "2",
    "component": {
      "limits": {
        "source": {
          "go_max_procs": 0
        }
      }
    },
    "componentIdx": "2"
  }
}
```

</details>

3. The `logfile` component checks in again reporting that it has succesfully applied the latest configuration.

```json
{
  "@timestamp": "2023-08-15T19:41:41.009Z",
  "observed": {
    "token": "27e63445-dff2-48a7-927d-3113ab50769b",
    "units": [
      {
        "id": "log-default-logfile-system-8ec9254c-0f1e-4166-965b-549c2818a0b0",
        "configStateIdx": "1",
        "message": "Starting"
      },
      {
        "id": "log-default",
        "type": "OUTPUT",
        "configStateIdx": "1",
        "state": "HEALTHY",
        "message": "Healthy"
      }
    ],
    "featuresIdx": "2",
    "componentIdx": "2"
  }
}
```

4. The agent responds with an abbreviated `CheckinExpected` message indicating the component is up to date.

```json
{
  "@timestamp": "2023-08-15T19:41:41.009Z",
  "expected": {
    "units": [
      {
        "id": "log-default-logfile-system-8ec9254c-0f1e-4166-965b-549c2818a0b0",
        "state": "HEALTHY",
        "configStateIdx": "1",
        "logLevel": "INFO"
      },
      {
        "id": "log-default",
        "type": "OUTPUT",
        "state": "HEALTHY",
        "configStateIdx": "1",
        "logLevel": "INFO"
      }
    ],
    "features": {
      "source": {
        "agent": {
          "features": {
            "fqdn": {
              "enabled": false
            }
          }
        }
      },
      "fqdn": {}
    },
    "featuresIdx": "2",
    "component": {
      "limits": {
        "source": {
          "go_max_procs": 0
        }
      }
    },
    "componentIdx": "2"
  },
}
```

### Sample Logging Code

To log control protocol messages in the format above in Go, code like the following can be used with
the [zap](https://github.com/uber-go/zap) logger. 

```go
    // When logging either the agent or client side of the exchange both an observed and expected response
    // are likely be available at the same time.
    var expected *proto.CheckinExpected,
    var observed *proto.CheckinObserved,

    obsJSON, _ := protojson.Marshal(observed)
    expJSON, _ := protojson.Marshal(expected)
    c.logger.Infow("Checkin",
        zap.Any("observed", json.RawMessage(obsJSON)),
        zap.Any("expected", json.RawMessage(expJSON)),
    )
```
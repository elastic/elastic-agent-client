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

## Example Exchange

TODO: Add an example message exchange between a component and the elastic agent.

## Developing

The development process is driven by [Mage](https://magefile.org/). Run `mage -l` to see the list of targets.

When editing the the .proto files first run `mage update:all` to run protoc followed by `mage check:all` to run the checks and tests.
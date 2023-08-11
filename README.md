# Elastic Agent Client
This repository implements the control protocol client for the [Elastic Agent](https://github.com/elastic/elastic-agent).
All processes implementing Elastic Agent inputs must implement a control protocol client.

The control protocol is implemented as gRPC service on localhost using port [6789](https://github.com/cmacknz/elastic-agent/blob/67313b282156f56010ea9ee236b3291cb1fea5ff/elastic-agent.yml#L167-L168) by default.

This repository currently contains three separate protobuf files:

* [elatic-agent-client.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client.proto) which implements the current
version of the protocol, usually referred to as V2. This version was introduced in Elastic Agent 8.6.0.
* [elastic-agent-client-deprecated.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client-deprecated.proto) which documents
the deprecated V1 protocol that was used until 8.6.0.
* [elastic-agent-client-future.proto](https://github.com/elastic/elastic-agent-client/blob/main/elastic-agent-client-future.proto) documents future extensions
of the V2 protocol that were reviewed as part of the original rewrite from V1 to V2 but never implemented.

The entrypoint for implementing a control protocol client in Go is to create an instance of the client by calling [client.NewV2()](https://github.com/elastic/elastic-agent-client/blob/c699c976fa3092435985dd633c1ed7807a753e74/pkg/client/client_v2.go#L224).

## Design
The design of the control protocol follows from the Elastic Agent [architecture](https://github.com/elastic/elastic-agent/blob/main/docs/architecture.md) which
defines the component and unit terminology used in the protocol.

The protocol today consists of two streaming RPCs:




## Bootstrapping

## Developing

TODO Add steps to regen the protocol here
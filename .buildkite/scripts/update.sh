#!/bin/bash

set -euo pipefail

source .buildkite/scripts/pre-install-command.sh
apt-get update
apt-get install unzip
curl -sSfL -o protoc.zip https://github.com/protocolbuffers/protobuf/releases/download/v3.19.4/protoc-3.19.4-linux-x86_64.zip
unzip -o protoc.zip -d /usr/local bin/protoc

add_bin_path
with_mage

mage -debug update

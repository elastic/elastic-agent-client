#!/bin/bash

set -euo pipefail

source .buildkite/scripts/pre-install-command.sh
echo "--- Prepare enviroment"
apt-get update
apt-get install unzip
curl -sSfL -o protoc.zip https://github.com/protocolbuffers/protobuf/releases/download/v3.19.4/protoc-3.19.4-linux-x86_64.zip
unzip -o protoc.zip -d /usr/local
which protoc
add_bin_path
with_mage
with_go_junit_report

echo "--- Update proto"
mage -debug update

echo "--- Run linters"
mage -debug check

echo "--- Run test"
set +e
go test -race ./...| tee tests-report.txt
exit_code=$?
set -e

# Create Junit report for junit annotation plugin
go-junit-report > junit-report.xml < tests-report.txt
exit $exit_code

#!/bin/bash

set -euo pipefail

source .buildkite/scripts/pre-install-command.sh
add_bin_path
with_go_junit_report

set +e
go test -race ./...| tee tests-report.txt
exit_code=$?
set -e

# Create Junit report for junit annotation plugin
go-junit-report > junit-report.xml < tests-report.txt
exit $exit_code

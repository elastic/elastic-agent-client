#!/bin/bash

set -euo pipefail

echo "--- Prepare enviroment"
source .buildkite/scripts/pre-install-command.sh
add_bin_path
with_mage

echo "--- Run linter"
mage -debug check

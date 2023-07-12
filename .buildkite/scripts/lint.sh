#!/bin/bash

set -euo pipefail

source .buildkite/scripts/pre-install-command.sh
add_bin_path
with_mage

mage -debug check

#!/usr/bin/env bash
# Build (release) and launch the Weft macOS app. The daemon must already be
# running: `weft serve ~/notes` (or `make run`).
set -euo pipefail
cd "$(dirname "$0")"
./build.sh release
open build/Weft.app

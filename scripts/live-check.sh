#!/bin/sh
# Reads the ten fixture papers with the real local model and prints the
# score: seconds per paper, dates and to-dos found against the ones a person
# wrote down, extras, guesses. Needs Ollama running with the model pulled.
#   scripts/live-check.sh                         # gemma4:e4b
#   BACKPACK_MODEL=gemma4:26b scripts/live-check.sh
set -eu
cd "$(dirname "$0")/.."
go test -tags live -run TestLiveFixtures -count=1 -timeout 60m -v ./internal/reader |
	sed -n '/^[[:space:]]*model /,/papers with a missed date/p' | sed 's/^[[:space:]]*//'

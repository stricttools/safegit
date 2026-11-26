#!/usr/bin/env bash
# Exhaustive, disjoint ordinary integration shards; no -short or stress opt-in.
set -euo pipefail
shard=${1:?shard required}
shards=${2:-4}
[[ $shard =~ ^[0-9]+$ && $shards =~ ^[1-9][0-9]*$ && $shard -lt $shards ]] || exit 2
mkdir -p .strictmetadata/validation
go test ./internal/test -list '^Test' > .strictmetadata/validation/unix-test-discovery.txt
awk '/^Test/ { print }' .strictmetadata/validation/unix-test-discovery.txt > .strictmetadata/validation/unix-test-names.txt
selected=$(awk -v shard="$shard" -v shards="$shards" '(NR-1)%shards==shard {print}' .strictmetadata/validation/unix-test-names.txt)
[[ -n $selected ]] || { echo 'Empty integration shard' >&2; exit 1; }
pattern="^($(printf '%s\n' "$selected" | paste -sd '|' -))$"
printf '%s\n' "$selected" > ".strictmetadata/validation/unix-selected-${shard}.txt"
go test ./internal/test -run "$pattern" -count=1 -timeout=35m -json | tee ".strictmetadata/validation/unix-integration-${shard}.jsonl"

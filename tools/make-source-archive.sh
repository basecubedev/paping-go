#!/bin/sh
set -eu

ref="${1:-HEAD}"
output="${2:-paping-go.tar.gz}"

git archive --format=tar.gz --output="$output" "$ref"

for blocked in '.git/' 'node_modules/' 'dist/' 'coverage.out'; do
	if tar -tzf "$output" | grep -q "^${blocked}"; then
		echo "Source archive contains unwanted path: ${blocked}" >&2
		exit 1
	fi
done

echo "Wrote clean source archive to ${output}"

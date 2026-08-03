#!/bin/sh

set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 SOURCE_REPOSITORY OUTPUT_REPOSITORY" >&2
	exit 1
fi

source_repository=$1
output_repository=$2

if [ "$source_repository" = "$output_repository" ]; then
	echo "source and output repositories must differ" >&2
	exit 1
fi

if [ ! -d "$source_repository/.git" ] || [ -L "$source_repository/.git" ]; then
	echo "source repository must contain a physical .git directory" >&2
	exit 1
fi

if [ ! -d "$output_repository" ]; then
	echo "output repository directory must exist" >&2
	exit 1
fi

if [ -n "$(find "$output_repository" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
	echo "output repository directory must be empty" >&2
	exit 1
fi

cp -a "$source_repository/." "$output_repository/"

if [ ! -d "$output_repository/.git" ] || [ -L "$output_repository/.git" ]; then
	echo "copied repository must contain a physical .git directory" >&2
	exit 1
fi

find "$output_repository/.git" -type f -name '*.lock' -exec rm -f -- {} +

#!/bin/sh
set -e +x

cd "$(dirname "$0")"

. ./build.sh

goenv exec go run github.com/Jorropo/jhttp

#!/bin/sh
set -e +x

cd "$(dirname "$0")"

set -x

GOOS=js GOARCH=wasm goenv exec go build -o main.wasm .
cp "$(goenv exec go env GOROOT)/misc/wasm/wasm_exec.js" wasm_exec.js

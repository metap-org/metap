#!/usr/bin/env sh
# Regenerates pkg/grpcapi/pb from these .proto files. Runs protoc in a container so no local
# toolchain is needed; versions match the headers of the committed .pb.go files.
set -eu
cd "$(dirname "$0")/.."
docker run --rm -v "$PWD":/w -w /w golang:1.26 sh -c '
  apt-get update -qq >/dev/null && apt-get install -y -qq protobuf-compiler >/dev/null &&
  go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 &&
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2 &&
  protoc -I proto --go_out=pb --go_opt=paths=source_relative \
    --go-grpc_out=pb --go-grpc_opt=paths=source_relative proto/*.proto &&
  chown -R '"$(id -u):$(id -g)"' pb'

#!/usr/bin/env bash
# End-to-end smoke test: build the image, deploy into an EXISTING kind cluster,
# and assert real telemetry reaches an OTLP sink. Proves what unit tests cannot —
# that the rendered config is one the collector accepts and that RBAC suffices.
#
# The kind cluster must already exist (CI creates it with a pinned node image via
# helm/kind-action; locally: `kind create cluster --name fixter-smoke`). This
# script operates on the current kubectl context so CI and local runs share it.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-ghcr.io/fixter-dev/fixter-collector}"
TAG="${TAG:-dev}"
CLUSTER="${CLUSTER:-fixter-smoke}"

./scripts/build.sh
docker build -t "${IMAGE}:${TAG}" .
kind load docker-image "${IMAGE}:${TAG}" --name "${CLUSTER}"

kubectl apply -f test/kind/sink.yaml
kubectl rollout status deployment/otlp-sink --timeout 2m

helm install fixter-collector charts/fixter-collector -f test/kind/values.yaml --wait --timeout 3m
kubectl rollout status daemonset/fixter-collector-agent --timeout 2m
kubectl rollout status deployment/fixter-collector-cluster --timeout 2m

./test/kind/assert-data.sh

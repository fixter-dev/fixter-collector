#!/usr/bin/env bash
# Validate the rendered CLUSTER collector config against the real binary.
#
# `validate` CONSTRUCTS every component rather than type-checking YAML, so a
# config is only validatable where its components' runtime preconditions hold.
# The AGENT config cannot be validated on a bare runner (host_metrics needs
# /hostfs, k8s_attributes needs K8S_NODE_NAME, kubelet_stats needs a real
# service-account) — the kind smoke test runs it for real in a Pod instead. This
# is the fast pre-check that catches unknown components and config-field typos.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=./_build/fixter-collector
[ -x "$BIN" ] || ./scripts/build.sh

# FIXTER_API_KEY must be set: bearertokenauth rejects an empty token at
# construction. The runtime always supplies it from the Secret.
helm template t charts/fixter-collector --set fixter.apiKey=dummy \
  | yq 'select(.kind=="ConfigMap" and (.metadata.name|test("cluster"))) | .data."config.yaml"' \
  > /tmp/cfg-cluster.yaml
FIXTER_API_KEY=dummy "$BIN" validate --config file:/tmp/cfg-cluster.yaml
echo "validate: cluster config OK"

# integrations.prometheus.scrapeConfigs is a verbatim passthrough, so nothing in the
# chart can tell a well-indented block from a structurally wrong one. Only the real
# receiver can. Renders both scrape forms together and constructs them for real.
helm template t charts/fixter-collector -f test/scrape-values.yaml \
  | yq 'select(.kind=="ConfigMap" and (.metadata.name|test("cluster"))) | .data."config.yaml"' \
  > /tmp/cfg-cluster-scrape.yaml
FIXTER_API_KEY=dummy "$BIN" validate --config file:/tmp/cfg-cluster-scrape.yaml
echo "validate: cluster config with prometheus scrape configs OK"

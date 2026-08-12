#!/usr/bin/env bash
set -euo pipefail

# `watch` mode only reports Events that occur after the receiver connects, and a
# freshly installed kind cluster is idle. Generate a pod lifecycle deliberately so
# there is something to observe, rather than depending on ambient cluster activity.
PROBE="smoke-event-probe"
cleanup() { kubectl delete pod "$PROBE" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT
kubectl delete pod "$PROBE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
kubectl run "$PROBE" --image=busybox:1.36 --restart=Never -- sh -c 'echo smoke' >/dev/null

echo "waiting up to 120s for telemetry to reach the sink..."
for i in $(seq 1 24); do
  LOGS=$(kubectl exec deployment/otlp-sink -c reader -- cat /data/sink.log 2>/dev/null || true)
  POD_NAMES=$(grep -c "k8s.pod.name" <<<"$LOGS" || true)
  CLUSTER_NAMES=$(grep -c "k8s.cluster.name" <<<"$LOGS" || true)
  EVENT_REASONS=$(grep -c "k8s.event.reason" <<<"$LOGS" || true)
  DATAPOINT_IDS=$(grep -A5 "Data point attributes" <<<"$LOGS" | grep -c "k8s.pod.name" || true)
  # otelcol_* exists nowhere else in this cluster: neither collector exports its own
  # internal telemetry, so these can only have come from scraping the sink's :8888.
  SCRAPED=$(grep -c "Name: otelcol_" <<<"$LOGS" || true)
  if [ "$POD_NAMES" -gt 0 ] && [ "$CLUSTER_NAMES" -gt 0 ] \
     && [ "$EVENT_REASONS" -gt 0 ] && [ "$DATAPOINT_IDS" -gt 0 ] \
     && [ "$SCRAPED" -gt 0 ]; then
    echo "PASS: telemetry arrived with k8s attributes, Events, datapoint identity, and scraped Prometheus metrics."
    exit 0
  fi
  sleep 5
done

echo "FAIL: no telemetry reached the sink in 120s."
echo "--- sink logs ---"; kubectl exec deployment/otlp-sink -c reader -- cat /data/sink.log 2>/dev/null | tail -50 || true
echo "--- agent logs ---"; kubectl logs daemonset/fixter-collector-agent --tail=50 || true
echo "--- cluster logs ---"; kubectl logs deployment/fixter-collector-cluster --tail=50 || true
echo "--- events check ---"; echo "${EVENT_REASONS:-0} Event records reached the sink"
echo "--- datapoint identity check ---"; echo "${DATAPOINT_IDS:-0} datapoints carried k8s.pod.name"
echo "--- prometheus scrape check ---"; echo "${SCRAPED:-0} otelcol_* series arrived from the scrape job"
exit 1

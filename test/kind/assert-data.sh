#!/usr/bin/env bash
set -euo pipefail

echo "waiting up to 120s for telemetry to reach the sink..."
for i in $(seq 1 24); do
  LOGS=$(kubectl exec deployment/otlp-sink -c reader -- cat /data/sink.log 2>/dev/null || true)
  POD_NAMES=$(grep -c "k8s.pod.name" <<<"$LOGS" || true)
  CLUSTER_NAMES=$(grep -c "k8s.cluster.name" <<<"$LOGS" || true)
  EVENT_REASONS=$(grep -c "k8s.event.reason" <<<"$LOGS" || true)
  DATAPOINT_IDS=$(grep -A5 "Data point attributes" <<<"$LOGS" | grep -c "k8s.pod.name" || true)
  if [ "$POD_NAMES" -gt 0 ] && [ "$CLUSTER_NAMES" -gt 0 ] \
     && [ "$EVENT_REASONS" -gt 0 ] && [ "$DATAPOINT_IDS" -gt 0 ]; then
    echo "PASS: telemetry arrived with k8s attributes, Events, and datapoint identity."
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
exit 1

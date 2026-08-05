#!/usr/bin/env bash
set -euo pipefail

echo "waiting up to 120s for telemetry to reach the sink..."
for i in $(seq 1 24); do
  LOGS=$(kubectl logs deployment/otlp-sink --tail=-1 2>/dev/null || true)
  if grep -q "k8s.pod.name" <<<"$LOGS" \
     && grep -q "k8s.cluster.name" <<<"$LOGS" \
     && grep -q "k8s.event.reason" <<<"$LOGS" \
     && grep -A5 "Data point attributes" <<<"$LOGS" | grep -q "k8s.pod.name"; then
    echo "PASS: telemetry arrived with k8s attributes, Events, and datapoint identity."
    exit 0
  fi
  sleep 5
done

echo "FAIL: no telemetry reached the sink in 120s."
echo "--- sink logs ---"; kubectl logs deployment/otlp-sink --tail=50 || true
echo "--- agent logs ---"; kubectl logs daemonset/fixter-collector-agent --tail=50 || true
echo "--- cluster logs ---"; kubectl logs deployment/fixter-collector-cluster --tail=50 || true
echo "--- events check ---"; grep -c "k8s.event.reason" <<<"$LOGS" || echo "0 Event records reached the sink"
echo "--- datapoint identity check ---"; grep -A5 "Data point attributes" <<<"$LOGS" | grep -c "k8s.pod.name" || echo "0 datapoints carried k8s.pod.name"
exit 1

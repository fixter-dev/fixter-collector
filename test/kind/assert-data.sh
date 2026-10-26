#!/usr/bin/env bash
set -euo pipefail

# `watch` mode only reports Events that occur after the receiver connects, and a
# freshly installed kind cluster is idle. Generate a pod lifecycle deliberately so
# there is something to observe, rather than depending on ambient cluster activity.
PROBE="smoke-event-probe"
ANNOTATED_JOB="smoke-annotated-job"
ANNOTATED_JOB_SERVICE="smoke-job-service"
PLAIN_JOB="smoke-plain-job"
cleanup() {
  kubectl delete pod "$PROBE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete job "$ANNOTATED_JOB" "$PLAIN_JOB" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup
ANNOTATED_SERVICE="smoke-annotated-service"
kubectl run "$PROBE" --image=busybox:1.36 --restart=Never \
  --annotations="resource.opentelemetry.io/service.name=${ANNOTATED_SERVICE}" \
  -- sh -c 'while true; do echo smoke; sleep 2; done' >/dev/null
kubectl apply -f - >/dev/null <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: ${ANNOTATED_JOB}
  annotations:
    resource.opentelemetry.io/service.name: ${ANNOTATED_JOB_SERVICE}
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: probe
          image: busybox:1.36
          command: ["sh", "-c", "echo smoke"]
EOF
kubectl create job "$PLAIN_JOB" --image=busybox:1.36 -- sh -c 'echo smoke' >/dev/null

service_and_job_of_created_pod_events() {
  awk -v job="$2" '
    /^Resource(Log|Metric|Span)s? #/ { svc = ""; jobname = "" }
    /-> service\.name: Str\(/ { svc = $0; sub(/.*Str\(/, "", svc); sub(/\)$/, "", svc) }
    /-> k8s\.job\.name: Str\(/ { jobname = $0; sub(/.*Str\(/, "", jobname); sub(/\)$/, "", jobname) }
    index($0, "Body: Str(Created pod: " job "-") { print svc "|" jobname }
  ' <<<"$1"
}

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
  ANNOTATED=$(grep -c "service.name: Str(${ANNOTATED_SERVICE})" <<<"$LOGS" || true)
  DERIVED=$(grep -c "service.name: Str(otlp-sink)" <<<"$LOGS" || true)
  ANNOTATED_JOB_EVENTS=$(service_and_job_of_created_pod_events "$LOGS" "$ANNOTATED_JOB")
  PLAIN_JOB_EVENTS=$(service_and_job_of_created_pod_events "$LOGS" "$PLAIN_JOB")
  ANNOTATED_JOB_NAMED=$(grep -cx "${ANNOTATED_JOB_SERVICE}|${ANNOTATED_JOB}" <<<"$ANNOTATED_JOB_EVENTS" || true)
  PLAIN_JOB_DERIVED=$(grep -cx "${PLAIN_JOB}|${PLAIN_JOB}" <<<"$PLAIN_JOB_EVENTS" || true)
  if [ -n "$ANNOTATED_JOB_EVENTS" ] && grep -qvx "${ANNOTATED_JOB_SERVICE}|${ANNOTATED_JOB}" <<<"$ANNOTATED_JOB_EVENTS"; then
    echo "FAIL: a Job Event of ${ANNOTATED_JOB} arrived under another service or without k8s.job.name:"
    echo "$ANNOTATED_JOB_EVENTS"
    exit 1
  fi
  if [ "$POD_NAMES" -gt 0 ] && [ "$CLUSTER_NAMES" -gt 0 ] \
     && [ "$EVENT_REASONS" -gt 0 ] && [ "$DATAPOINT_IDS" -gt 0 ] \
     && [ "$SCRAPED" -gt 0 ] && [ "$ANNOTATED" -gt 0 ] && [ "$DERIVED" -gt 0 ] \
     && [ "$ANNOTATED_JOB_NAMED" -gt 0 ] && [ "$PLAIN_JOB_DERIVED" -gt 0 ]; then
    echo "PASS: telemetry arrived with k8s attributes, Events, datapoint identity, scraped Prometheus metrics, annotated and derived service names, and Job Events named by the Job's annotation or after the Job."
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
echo "--- service name check ---"; echo "${ANNOTATED:-0} records named ${ANNOTATED_SERVICE} by annotation, ${DERIVED:-0} named otlp-sink by its Deployment"
echo "--- job event check ---"; echo "${ANNOTATED_JOB_NAMED:-0} Created pod Events of ${ANNOTATED_JOB} named ${ANNOTATED_JOB_SERVICE}, ${PLAIN_JOB_DERIVED:-0} of ${PLAIN_JOB} named after the Job"
echo "annotated: ${ANNOTATED_JOB_EVENTS:-none}"; echo "plain: ${PLAIN_JOB_EVENTS:-none}"
exit 1

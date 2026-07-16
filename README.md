# fixter-collector

A Fixter OpenTelemetry Collector distribution and Helm chart. Collects Kubernetes
metrics and pod logs, relays your apps' OTLP, and ships it all to Fixter.

## Install

```bash
helm install fixter-collector \
  oci://ghcr.io/fixter-dev/charts/fixter-collector \
  --namespace fixter --create-namespace \
  --set fixter.apiKey=<your-key>
```

That's it — `fixter.apiKey` is the only required value. `fixter.endpoint`
already defaults to `https://ingest.fixter.dev`. Without `--version` this pulls
the latest published chart; pin one with `--version <x.y.z>` for reproducible
deploys.

Get a key at https://fixter.dev → Settings → API Keys.

### Verify the key

A bad key otherwise fails silently — the collector starts, reports healthy,
and sends nothing. Run the bundled test right after install:

```bash
helm test fixter-collector -n fixter
```

This only proves the API key is valid and the endpoint is reachable (it POSTs
an empty metrics payload and checks the HTTP status). It does **not** prove
every export succeeds — a collector that starts fine but fails exports for
some other reason (bad downstream, TLS, quota) still reports healthy.

## Multiple clusters

`fixter.clusterName` is **not auto-detected** — there is no EKS/AKS/GKE
lookup. If you run more than one cluster and want to tell them apart in
Fixter, set it explicitly:

```bash
--set fixter.clusterName=my-cluster
```

Leave it unset and `k8s.cluster.name` is simply absent from your telemetry —
fine for a single cluster, ambiguous for more than one.

## What it deploys

| Component | Kind | Collects |
|---|---|---|
| `agent` | DaemonSet | kubelet + host metrics, pod logs, OTLP relay on `:4317`/`:4318` |
| `cluster` | Deployment (1 replica) | cluster-state metrics, Prometheus targets |

`cluster` runs exactly one replica and this is not configurable — its receivers
duplicate their output per replica, so scaling it double-counts your data rather
than adding availability.

The agent tolerates all taints by default, so tainted nodes are not silently
skipped.

## Service attribution

Fixter groups every signal by service, but pod logs (read from files) and
kubelet/pod metrics carry no `service.name` of their own. The agent fills the
service-identity triad — `service.name` (from the k8s deployment, falling back to
the container name), `service.namespace` (from the k8s namespace), and
`service.instance.id` (from the pod name) — on pod logs and metrics, using the
Kubernetes metadata it already attaches.

This only ever *fills a missing* value: telemetry your apps relay through the
agent keeps the `service.name` they set, and node/host metrics with no owning
deployment stay serviceless (as infra metrics should).

## Sending app telemetry

Point your apps at the agent's Service — named `<release-name>-fixter-collector-agent`
(e.g. installing as `fixter-collector` gives you `fixter-collector-agent`):

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://fixter-collector-agent.fixter.svc.cluster.local:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

Traffic stays on the node (`internalTrafficPolicy: Local`).

## Log volume

Pod logs are on by default. `kube-system` and the collector's own pods are
excluded already. ~216-byte JSON lines at 10k logs/s ≈ 2.2 MB/s ≈
**186 GB/day**.

Trim it further:

```yaml
agent:
  logs:
    excludeNamespaces: [kube-system, istio-system]
    excludePods: [chatty-app]
```

Or disable pod log collection entirely:

```bash
--set agent.logs.enabled=false
```

## Log severity and stack traces

Pod log lines carry no severity, and a stack trace arrives as one line per
frame. The agent derives both: JSON bodies are parsed as JSON and take their
level from the `level` field; anything else falls back to a regex over the
text. Lines that don't start a new record (stack frames, wrapped messages) are
joined onto the record above.

Both paths are best-effort — a format neither recognises still ships, just
without a severity. Nothing is ever dropped for being unparseable.

If your logs use a different shape, point the patterns at it:

```yaml
agent:
  logs:
    parsing:
      # A line matching this starts a NEW record; anything else is appended to
      # the record above.
      firstEntryRegex: '^(\{|\[|\d{4}-\d{2}-\d{2})'
      json:
        severityField: level          # e.g. severity_text, levelname
      text:
        severityRegex: '^\S+\s+(?P<severity>TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL)\b'
```

If your app's lines start with none of `firstEntryRegex`'s alternatives, every
line would be appended to one record — give it a pattern that matches your
format. `forceFlushPeriod` (5s) and `maxLogSize` (1MiB) bound that case rather
than fix it.

Or leave logs raw and unparsed:

```bash
--set agent.logs.parsing.enabled=false
```

## Bring your own Secret

```bash
--set fixter.existingSecret=my-secret --set fixter.existingSecretKey=api-key
```

Mutually exclusive with `fixter.apiKey`.

## Scraping Prometheus endpoints

```yaml
integrations:
  prometheus:
    targets:
      - job: my-service
        endpoints: ["my-service.default.svc.cluster.local:9090"]
        interval: 30s
```

## Security

- The **`cluster`** collector runs fully hardened and non-root:
  `runAsNonRoot: true`, `runAsUser`/`runAsGroup: 10001`,
  `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`,
  `seccompProfile: RuntimeDefault`, and all Linux capabilities dropped.
- The **`agent`** runs as **root** (`runAsUser: 0`, `runAsNonRoot: false`) by
  necessity: `/var/log/pods` on EKS and most containerd distros is root-owned,
  so a non-root agent silently reads **nothing** — `file_log` starts, opens no
  file, logs no error, and the pod stays Ready while collecting zero logs. Every
  log-collecting DaemonSet (Fluent Bit, Datadog, Vector) runs as root for the
  same reason. The agent is still hardened at the container level:
  `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`,
  `seccompProfile: RuntimeDefault`, all capabilities dropped.
- The agent mounts `/var/log/pods` and the host root filesystem (`/`) via
  `hostPath`, both **read-only**, to collect pod logs and host-level metrics.
  This is inherent to node-level collection and is not configurable away.
- A `restricted` Pod Security Admission namespace will reject the agent
  DaemonSet — both for the hostPath mounts and because it runs as root. Install
  into a namespace that permits it — `--create-namespace` above gives you a
  plain one.
- Both the agent and cluster collector tolerate all taints by default, so
  tainted nodes/singletons aren't silently skipped.

## Requirements

Kubernetes >= 1.23.

## Contents at a glance

The chart currently ships:

- `agent` (DaemonSet): kubelet + host metrics, pod log tailing, OTLP relay
- `cluster` (single-replica Deployment): cluster-state (`k8s_cluster`)
  metrics and any configured Prometheus scrape targets

Datastore integrations (ClickHouse, Doris, PostgreSQL, MySQL, Redis) and
cloud-native ingest paths (AWS Firehose, Azure Event Hub, GCP) are not yet
part of this chart.

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

`fixter.apiKey` is the only required value. `fixter.endpoint` already defaults to
`https://ingest.fixter.dev`. Without `--version` this pulls the latest published
chart; pin one with `--version <x.y.z>` for reproducible deploys.

Get a key at https://app.fixter.dev → Settings → API Keys.

### Verify the key

A bad key fails silently — the collector starts, reports healthy, and sends
nothing. Run the bundled test right after install:

```bash
helm test fixter-collector -n fixter
```

This proves only that the API key is valid and the endpoint is reachable (it
POSTs an empty metrics payload and checks the HTTP status). It does **not** prove
every export succeeds — a collector that fails exports for some other reason (bad
downstream, TLS, quota) still reports healthy.

## Multiple clusters

`fixter.clusterName` is **not auto-detected** — there is no EKS/AKS/GKE lookup.
If you run more than one cluster and want to tell them apart in Fixter, set it:

```bash
--set fixter.clusterName=my-cluster
```

Leave it unset and `k8s.cluster.name` is simply absent — fine for a single
cluster, ambiguous for more than one.

## What it deploys

| Component | Kind | Collects |
|---|---|---|
| `agent` | DaemonSet | kubelet + host metrics, pod logs, OTLP relay on `:4317`/`:4318` |
| `cluster` | Deployment (1 replica) | cluster-state metrics, Prometheus targets |

`cluster` runs exactly one replica and this is not configurable — its receivers
duplicate their output per replica, so scaling it double-counts data. Both
workloads tolerate all taints by default, so tainted nodes aren't silently
skipped.

## Sending app telemetry

Point your apps at the agent's Service — named `<release>-fixter-collector-agent`
(installing as `fixter-collector` gives you `fixter-collector-agent`):

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://fixter-collector-agent.fixter.svc.cluster.local:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

Traffic stays on the node (`internalTrafficPolicy: Local`).

### Service attribution

Fixter groups every signal by service, but pod logs and kubelet/pod metrics carry
no `service.name` of their own. The agent fills the service-identity triad —
`service.name` (from the k8s deployment, falling back to the container name),
`service.namespace` (from the k8s namespace), and `service.instance.id` (from the
pod name) — from the Kubernetes metadata it already attaches. This only ever
*fills a missing* value: telemetry your apps relay keeps the `service.name` they
set, and node/host metrics stay serviceless (as infra metrics should).

## Log volume

Pod logs are on by default. `kube-system` and the collector's own pods are
excluded already. As a rough scale: ~216-byte JSON lines at 10k logs/s ≈ 2.2 MB/s
≈ **186 GB/day**. Trim further:

```yaml
agent:
  logs:
    excludeNamespaces: [kube-system, istio-system]
    excludePods: [chatty-app]
```

Or turn pod log collection off entirely:

```bash
--set agent.logs.enabled=false
```

## Log severity and stack traces

A pod log line carries no severity, and a stack trace arrives as one line per
frame. What the agent does about that depends on whether it knows the format.

**Out of the box**, it reads only what a line's own shape reveals — no guessing:

| Format | Severity | Record boundaries |
| --- | --- | --- |
| JSON, string level (zap, winston, logback) | yes — from `level` | one per line |
| JSON, numeric level (pino, bunyan) | yes — `10`…`60` mapped | one per line |
| glog / klog — Doris BE, Kubernetes components | yes — from the `I`/`W`/`E`/`F` prefix | one per line |
| any other text format | none | one per line |

That last row is deliberate. For a format it doesn't know, the agent can't find a
level word or a record boundary without guessing — and a wrong severity is worse
than none (a service named `trace-service` read as TRACE, a 200-OK nginx line with
`/error/` read as ERROR). It also never joins continuation lines on an unknown
format, because a wrong join merges *unrelated events* into one record and hides
their severities. Nothing is ever dropped for being unparseable — an unrecognised
line still ships, just without a severity.

### Datastores work out of the box

Common datastores are recognised automatically — no config — because they log
under predictable container or pod names. These `builtinFormats` are enabled by
default and give those pods proper severity and stack-trace joining:

| Detected by | Format |
| --- | --- |
| container `clickhouse` / `clickhouse-keeper` | `clickhouse` |
| pod `doris-fe-*` (Doris BE is glog, handled above) | `doris-fe` |
| container `postgres` / `postgresql` | `postgres` |
| container `mysql` | `mysql` |
| container `kafka` | `kafka` |
| container `aws-node` | `logfmt` |

Disable any one you don't want:

```bash
--set agent.logs.builtinFormats.mysql.enabled=false
```

### Telling it about your own format, with `formats`

For a text format the agent doesn't know, point a preset at the pods that emit it.
Inside a format-scoped receiver the format is *known*, so its pattern is anchored
to that format's structure and can't be fooled:

```yaml
agent:
  logs:
    formats:
      - preset: spring
        include: ["/var/log/pods/prod_*/*/*.log"]
      - preset: python
        include: ["/var/log/pods/*_worker-*/*/*.log"]
```

Built-in presets:

| preset | reads |
| --- | --- |
| `spring` | Spring Boot 2.x / 3.x |
| `clickhouse` | ClickHouse server |
| `postgres` | PostgreSQL (`log_line_prefix` default, and `%q%u@%d`) |
| `mysql` | MySQL 8 error log |
| `kafka` | Kafka / log4j `[date] LEVEL` |
| `doris-fe` | Doris FE (Doris BE is glog — no preset needed) |
| `logfmt` | any `key=value` line — go-kit, logrus non-TTY, `slog` `TextHandler` |
| `python` | `logging.basicConfig()` default, plus the two common `%(asctime)s` overrides |
| `dotnet` | default console logger (`Simple`), including its two-line events |
| `go-stdlib` | Go's stdlib `log` — **joins records only, no severity** |
| `zap-console` | zap's console encoder (`zap.NewDevelopment()`) |

Each preset is covered by a regression test that builds the chart's own receiver
over a corpus and checks the record count and `{body, severity}` of every record
(`go test ./test/formats`; see `test/formats/README.md` for what it does and does
not prove). A format entry sets `preset: <name>` plus `include`; any other key you
set overrides the preset's, so a preset is a starting point, not a cage.

**Selection is by log-file path** — `/var/log/pods/<namespace>_<pod>_<uid>/<container>/0.log`
— because that is the only thing available at parse time. Pod labels and
annotations arrive via `k8s_attributes`, a *processor* that runs after the
receiver has parsed the line, so a format is targeted by namespace, pod-name
prefix, or container, as above.

**Order matters — first match wins.** Each format automatically excludes the globs
of every format before it, so a file matching several is read only by the
earliest. Put narrow formats before broad ones. Every format's globs are also
excluded from the catch-all automatically, so no file is read twice — **do not add
those excludes by hand**; two receivers reading one file double every record and
the ingest bill.

A format with no preset spells itself out:

```yaml
      - name: myapp
        include: ["/var/log/pods/myapp_*/*/*.log"]
        continuationRegex: '^(\s|Caused by:|\.\.\.\s\d+\smore)'
        severityRegex: '^\S+ \S+ +(?P<severity>[A-Z]+) '
        severityMapping: {}
```

Levels outside the six OTel names — ClickHouse's `<Information>`, MySQL's `[Note]`,
Postgres' `LOG`, pino's numeric `30` — are resolved through `severityMapping`.

**Describe the continuation, not the record start — and bound it.** The recombine
engine has no "neither" state: every line the predicate doesn't match is appended
to the record above. Set `continuationRegex` (a line that CONTINUES the record),
not `firstEntryRegex` — a continuation pattern goes wrong only if *you* write it
too broad, in review where it's caught, whereas a start pattern silently breaks
when someone changes an unseen log-format setting. Anchor it to shapes the
*runtime* emits (a stack frame, a `Caused by:` header) — never to something as
open as "an identifier at column 0". Splitting a record is recoverable; merging
unrelated events into one, hiding an ERROR inside an INFO, is not. This is why
every preset ships a `continuationRegex` and changed direction in 0.2.0.

Setting both `firstEntryRegex` and `continuationRegex` is rejected at template
time. Set `continuationRegex: null` to drop a preset's predicate. Two more keys
bound the recombine (both apply only when a format recombines):

| key | default | what it does |
| --- | --- | --- |
| `forceFlushPeriod` | `5s` | How long an unterminated record stays open before it's emitted anyway. Raise it if a slow stack trace is cut in half; lower it to reduce lag to Fixter. |
| `maxLogSize` | `1MiB` | Size cap on one recombined record — a record hitting it is flushed as-is, so a runaway trace can't grow without bound. |

### Kubernetes logs on EKS

The glog parsing above covers klog, but on EKS most Kubernetes logs aren't pod
logs at all, so the agent never sees them:

| Source | Where its logs go | Reachable? |
| --- | --- | --- |
| Control plane (apiserver, etcd, scheduler, controllers) | CloudWatch — AWS-managed, not pods | **no** — needs an AWS receiver this distro doesn't build |
| kubelet, containerd | journald on the node | **no** — needs a journald receiver this distro doesn't build |
| kube-system pods (CoreDNS, aws-node, Karpenter, external-dns, CSI, LB controller) | `/var/log/pods` | yes — but **excluded by default** |

Only the third is available, and `excludeNamespaces` drops it for volume. To
collect it (Karpenter and external-dns are usually the ones worth having), remove
`kube-system`:

```yaml
agent:
  logs:
    excludeNamespaces: []
```

There is no AWS or journald log support — the distro builds no such receivers
(only `filelog`, `hostmetrics`, `k8scluster`, `kubeletstats`, `otlp`,
`prometheus`).

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

- **`cluster`** runs fully hardened and non-root: `runAsNonRoot: true`,
  `runAsUser`/`runAsGroup: 10001`, `readOnlyRootFilesystem: true`,
  `allowPrivilegeEscalation: false`, `seccompProfile: RuntimeDefault`, all
  capabilities dropped.
- **`agent`** runs as **root** (`runAsUser: 0`) by necessity: `/var/log/pods` on
  EKS and most containerd distros is root-owned, so a non-root agent silently
  reads **nothing** — `filelog` starts, opens no file, logs no error, and the pod
  stays Ready collecting zero logs. Every log-collecting DaemonSet (Fluent Bit,
  Datadog, Vector) runs as root for the same reason. The agent is still hardened
  at the container level: `readOnlyRootFilesystem: true`,
  `allowPrivilegeEscalation: false`, `seccompProfile: RuntimeDefault`, all
  capabilities dropped.
- The agent mounts `/var/log/pods` and the host root filesystem (`/` at
  `/hostfs`) via `hostPath`, both **read-only**, for pod logs and host metrics.
  This is inherent to node-level collection and not configurable away.
- A `restricted` Pod Security Admission namespace rejects the agent — for the
  hostPath mounts and for running as root. Install into a namespace that permits
  it; `--create-namespace` above gives you a plain one.

## Requirements

Kubernetes >= 1.23.

## What's not here yet

The chart ships log collection and Kubernetes/host metrics. It does **not** yet
build receivers for:

- **Datastore metrics** (ClickHouse, Doris, PostgreSQL, MySQL, Redis) — their
  *logs* are parsed today (see [Datastores work out of the box](#datastores-work-out-of-the-box)),
  but there are no dedicated metrics receivers.
- **Cloud-native ingest** (AWS CloudWatch / Firehose, Azure Event Hub, GCP) — so
  managed services like RDS, whose logs and metrics live in CloudWatch rather than
  in pods, are not reachable yet.

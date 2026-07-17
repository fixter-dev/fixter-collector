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
frame. What the agent can do about that depends on whether it knows the format.

**Out of the box** it reads only what a line's own SHAPE reveals, with no
guessing:

| Format | Severity | Record boundaries |
| --- | --- | --- |
| JSON, string level (zap, winston, logback) | yes — from the `level` field | one per line, which is correct for JSON |
| JSON, numeric level (pino, bunyan) | yes — `10`…`60` are mapped | as above |
| glog / klog — Doris BE, every Kubernetes component | yes — from the `I`/`W`/`E`/`F` prefix | one per line |
| **any other text format** | **none** | **one per line** |

That last row is deliberate, and it is the honest answer rather than a
limitation to work around. The catch-all reads files whose format it does not
know, so it cannot find a level word without guessing at where one sits — and
guessing gets it wrong in both directions: a positional search read a service
named `trace-service` as TRACE, and a 200-OK nginx line containing `/error/` as
ERROR. **A wrong severity is worse than none: it is invisible to alerting either
way, but it also lies.** For the same reason it never joins continuation lines:
the multiline engine takes a single predicate with no "neither" state, so on an
unrecognised format it merges *unrelated events* into one record. A split stack
trace is recoverable; a merged one, or a lie, is not.

**Nothing is ever dropped for being unparseable** — an unrecognised line still
ships, just without a severity.

### Telling it a format, with `formats`

To get severity and stack traces from a text format, point a format at the pods
that emit it. Inside a format-scoped receiver the format is *known*, so its
pattern is anchored to that format's own structure and cannot be fooled:

```yaml
agent:
  logs:
    formats:
      - preset: spring
        include: ["/var/log/pods/prod_*/*/*.log"]
      - preset: clickhouse
        include: ["/var/log/pods/clickhouse_*/*/*.log"]
      - preset: postgres
        include: ["/var/log/pods/*_postgres-*/*/*.log"]
```

Built-in presets:

| preset | reads |
| --- | --- |
| `spring` | Spring Boot 2.x and 3.x |
| `clickhouse` | ClickHouse server |
| `postgres` | PostgreSQL (`log_line_prefix` default, and `%q%u@%d`) |
| `mysql` | MySQL 8 error log |
| `doris-fe` | Doris FE (Doris BE is glog — no preset needed) |
| `logfmt` | any `key=value` line — go-kit, logrus' default non-TTY mode, Go's `log/slog` `TextHandler` |
| `python` | `logging.basicConfig()`'s default, plus the two common `%(asctime)s` overrides |
| `dotnet` | the default console logger (`Simple`), including its two-line events |
| `go-stdlib` | Go's standard-library `log` — **joins records only, no severity** |
| `zap-console` | zap's console encoder (`zap.NewDevelopment()`) |

Each is covered by a regression test that builds the chart's own receiver over a
corpus and checks the record count and the {body, severity} pair of every record
it produces (`go test ./test/formats`; see `test/formats/README.md` for exactly
what that does and does not prove — the `doris-fe` and `logfmt` corpora are written from
those formats' documented default patterns and the `dotnet` one from the .NET
runtime's formatter source, rather than captured from a running emitter; the rest
are real output). A format entry sets
`preset: <name>` plus `include`; any other key you set on the entry overrides
the preset's, so a preset is a starting point rather than a cage.

**Selection is by log file path** — `/var/log/pods/<namespace>_<pod>_<uid>/<container>/0.log` —
because that is the only thing available at parse time. Pod labels and
annotations are **not** usable here: they arrive via `k8s_attributes`, which is a
*processor* and runs after the receiver has already parsed the line. (Vector has
the identical constraint.) So a format is targeted by namespace, pod-name prefix
or container, as above.

**Order matters — first match wins.** Each format automatically excludes the
globs of every format declared before it, so a file matching several formats is
read only by the earliest one. Put narrow formats before broad ones: a broad
`prod_*` ahead of a specific `prod_api_*` swallows the API pods and the narrow
format below it goes dead, silently.

Every format's globs are also excluded from the catch-all automatically, so no
file is ever read twice. **Do not add those excludes by hand** — that is the
chart's job, and two receivers reading one file duplicates every record and
doubles the ingest bill.

A format with no preset spells it out itself:

```yaml
      - name: myapp
        include: ["/var/log/pods/myapp_*/*/*.log"]
        continuationRegex: '^(\s|Caused by:|\.\.\.\s\d+\smore)'
        severityRegex: '^\S+ \S+ +(?P<severity>[A-Z]+) '
        severityMapping: {}
```

Levels that are not one of the six OTel names — ClickHouse's `<Information>`,
MySQL's `[Note]`, Postgres' `LOG`, pino's numeric `30` — are resolved through
`severityMapping`. Add your own there rather than widening the regex.

`firstEntryRegex` and `continuationRegex` are the two directions of one
predicate — a record's START, or a line that CONTINUES the record above.
Recombine takes only one, so setting both is rejected at template time rather
than silently ignoring one; note every preset that recombines already ships a
`continuationRegex`, so `preset: <name>` plus your own `firstEntryRegex` trips
that rejection. Set `continuationRegex: null` to replace a preset's predicate.
(`logfmt` is the exception: it ships neither, being one-line by construction.)

**Describe the continuation, not the record start — and bound it.** Recombine has
no third state, so every line the predicate does not match is appended to the
record above. A predicate can be wrong two ways, and the direction only helps
with one of them:

| | pattern fits | pattern too **narrow** | pattern too **broad** |
| --- | --- | --- | --- |
| `firstEntryRegex` | joins correctly | **nothing starts a record — the entire stream merges into one blob, stamped with the first line's severity** | fragments: ordinary lines each start a record |
| `continuationRegex` | joins correctly | fragments: one record per line | **ordinary lines are swallowed into the record above — the same merge, the same destroyed data** |

Splitting a record is recoverable — you read two records instead of one, and
every line keeps its own severity. Merging unrelated events is not: the lines
are concatenated into a neighbour and an ERROR disappears inside an INFO,
invisible to every alert.

`continuationRegex` is the direction to reach for because a pattern goes
**narrow on its own** — someone changes a log-format setting you never see —
while it only goes **broad if you write it broad**, in review, where it can be
caught. That is the whole argument. It is not that continuation cannot destroy
data: a too-broad continuation destroys it exactly as thoroughly. Three presets
here shipped that bug (an unbounded dotted-identifier branch merged the whole
stream under a logger-first layout) and it was caught by a corpus, not by the
direction. Bound your pattern to shapes your format's *runtime* emits — a stack
frame, a chained-cause header, an exception-suffixed class name — never to
something as open as "an identifier at column 0".

This is not theoretical, and it is why every preset changed direction in 0.2.0.
A format-scoped receiver knows the *software*, not the software's *log-format
configuration* — and every format has a knob that moves its record start
(Spring's `logging.pattern.dateformat`, Postgres' `log_line_prefix`, Python's
`datefmt=`, Go's `log.SetFlags`, .NET's `TimestampFormat`) while none of them
moves its *continuation* shape, because stack frames come from the language
runtime rather than the log config. Measured on the real binary: `preset: spring`
against a Boot app setting `logging.pattern.dateformat` turned three independent
events into **one** record under `Info(9)`, hiding an ERROR. The continuation is
the stable half of a format; anchor to it.

Two more per-format keys bound the recombine (both apply only when the format
recombines at all):

| key | default | what it does |
| --- | --- | --- |
| `forceFlushPeriod` | `5s` | How long an unterminated record may stay open before it is emitted anyway. Raise it if a slow stack trace is being cut in half; lower it to reduce the lag before a record reaches Fixter. |
| `maxLogSize` | `1MiB` | Size cap on one recombined record. A record hitting it is flushed as-is, so a runaway trace cannot grow without bound. |

#### Python, .NET and Go

These three fragmented worst — a Python traceback or a Go panic became one
severity-less record per line — so each now has a preset:

```yaml
agent:
  logs:
    formats:
      - preset: python
        include: ["/var/log/pods/*_worker-*/*/*.log"]
      - preset: dotnet
        include: ["/var/log/pods/*_api-*/*/*.log"]
      - preset: go-stdlib
        include: ["/var/log/pods/*_operator-*/*/*.log"]
```

- **`python`** reads `logging.basicConfig()`'s default (`ERROR:__main__:msg`),
  and joins the `Traceback`/`File`/`KeyError: 'id'` lines under it into one
  record — including that last line, which is *not* indented. It also reads the
  two common `%(asctime)s` overrides (`... - name - ERROR - msg` and
  `... ERROR msg`). A `format=` with **neither** a leading level **nor** a
  leading timestamp is not covered.
- **`dotnet`** reads the default console logger, whose event is *two* lines
  (`fail: Category[1]` then a six-space-indented message) — so it fixes a
  fragmentation that exists even without a stack trace. All six levels are
  mapped, including `fail` → Error and `crit` → Fatal.
- **`go-stdlib`** reads Go's standard-library `log` (`2026/07/16 09:00:00 msg`)
  and **reports no severity at all** — stdlib `log` emits no level, so there is
  nothing to read and this preset does not invent one. It is still worth
  enabling: it keeps `panic:` + the `[signal ...]` line + the `goroutine` dump in
  **one** record, instead of one record per line for you to reassemble by eye.
  It joins on the *panic dump's* shape, so `panic:` itself starts a record rather
  than merging backwards into the log line above it and inheriting its identity.
- **`zap-console`** reads zap's console encoder, joining on its *stack-frame*
  shape. Every field of a record header here is an `EncoderConfig` knob — the
  level's case and the timestamp's format both vary with the config, and dropping
  the time encoder removes the timestamp altogether — whereas
  `zapcore.NewStacktrace` emits frames the same way regardless.

**Most Go logging needs no preset at all**, because its default is already
structured and is read structurally:

| library | default output | covered by |
| --- | --- | --- |
| `log/slog` (`JSONHandler`), zap (`NewProduction`), zerolog | JSON | structural JSON — nothing to configure |
| `log/slog` (`TextHandler`), logrus (non-TTY), go-kit | logfmt | `preset: logfmt` |
| stdlib `log` | `2026/07/16 09:00:00 msg` | `preset: go-stdlib` (joining only) |
| zap console encoder | tab-delimited | `preset: zap-console` |

**Not covered: zerolog's `ConsoleWriter`.** It colorizes by default *even when
stdout is redirected*, so real container output wraps the level in ANSI escapes
(`\x1b[32mINF\x1b[0m`) — and zerolog's own default is JSON, which already
resolves structurally. Use the default; don't put `ConsoleWriter` in production.
logrus with colors forced on is unread for the same reason.

Emitting JSON or logfmt is still the better fix for any service you control —
both are read structurally, with no glob to maintain.

There is no AWS log support because there is nothing to support: the distro
builds no AWS receivers (`filelog`, `hostmetrics`, `k8scluster`, `kubeletstats`,
`otlp`, `prometheus`).

### What "Kubernetes logs" actually reaches you

The parsing above handles klog, but on EKS most Kubernetes logs are not pod logs
at all, so the agent never sees them:

| Source | Where its logs go | Reachable? |
| --- | --- | --- |
| Control plane (apiserver, etcd, scheduler, controller-manager) | CloudWatch — AWS-managed, not pods | **no** — needs an AWS receiver this distro does not build |
| kubelet, containerd | journald on the node | **no** — needs a journald receiver this distro does not build |
| kube-system pods (CoreDNS, aws-node, Karpenter, external-dns, EBS CSI, LB controller) | `/var/log/pods` | yes — but **excluded by default** |

Only the third class is available, and `excludeNamespaces` drops it by default
for volume. To collect it, remove `kube-system` from the list:

```yaml
agent:
  logs:
    excludeNamespaces: []
```

Karpenter and external-dns are the ones usually worth having: they explain node
provisioning and DNS changes, which is exactly what you want when scheduling
misbehaves.

Or leave logs entirely raw — no severity at all, not even the structural ones:

```bash
--set agent.logs.structural.json.enabled=false \
--set agent.logs.structural.glog.enabled=false
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

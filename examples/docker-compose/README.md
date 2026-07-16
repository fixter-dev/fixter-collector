# Docker Compose example

Run the Fixter collector next to your app in a Compose stack. Your app sends OTLP
to the collector by service name; the collector forwards it to Fixter over
authenticated OTLP/HTTP.

This is the OTLP relay the Helm chart runs on Kubernetes, reduced to what a Compose
stack needs — no Kubernetes metrics, no pod-log scraping. If you only need to ship
your app's own traces/logs/metrics and don't want to run a collector at all, skip
this and export straight to `https://ingest.fixter.dev` from the app.

## Use it

```bash
FIXTER_API_KEY=<your-key> docker compose up
```

Then point your app at the collector:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://fixter-collector:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_SERVICE_NAME=<your-app>
```

`FIXTER_ENDPOINT` defaults to `https://ingest.fixter.dev`; override it if you send
to a different Fixter environment.

## Files

- `docker-compose.yaml` — the collector service (and a commented-out app stub).
- `otel-config.yaml` — the collector config: OTLP in, authenticated OTLP/HTTP out.

## Verify

The config validates against the published image, and a trace sent to the collector
is forwarded to the configured endpoint with the bearer token attached:

```bash
docker run --rm -e FIXTER_API_KEY=x -e FIXTER_ENDPOINT=https://ingest.fixter.dev \
  -v "$PWD/otel-config.yaml:/etc/otel-config.yaml:ro" \
  ghcr.io/fixter-dev/fixter-collector:0.1.1 validate --config=file:/etc/otel-config.yaml
```

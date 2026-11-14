# Traces

Traces are in Tempo (`system/tempo`) for 7 days (1 in the sandbox). Grafana → Explore → data source Tempo.

## What sends spans

| Source | How |
|---|---|
| The Istio Gateways (`gateway`, `public`) | Istio's Telemetry API (`system/istio-system/templates/telemetry.yaml`): every request in the sandbox, a tenth in production |
| Argo CD | `otlp.address` in its params (`system/argocd/values.yaml`) |
| Grafana | `tracing.opentelemetry.otlp` in `grafana.ini` (`platform/grafana/values.yaml`) |

Everything goes to the gateway Collector (`gateway-collector.opentelemetry`, OTLP gRPC 4317 or HTTP 4318), which only
the senders above may call (`allow-otlp` in `system/opentelemetry`: OTLP is never published). A new sender adds its
namespace to `gateway.senders` in `system/opentelemetry/values.yaml`.

Not traced: the ambient mesh's ztunnel makes no spans (L4 only), so calls between services inside the mesh show no
span until waypoints come; Loki, Tempo and Kanidm send none (their own telemetry would mostly trace the telemetry path,
and Kanidm's OTLP is not in kaniop's resources). Apps of our own get an SDK through the Operator's `Instrumentation`
(Backstage, the next step, is the first).

## From a log line to its trace, and back

The Gateways write a JSON access log line per request with its `trace_id`. In Explore with Loki:

```logql
{k8s_namespace_name="istio-system"} |= "/api/health"
```

Each line's `trace_id` field is a link that opens the trace in Tempo; a trace in Tempo has "Logs for this span", which
queries Loki for its trace id.

A request can carry its own trace id (W3C `traceparent`), to find it later:

```sh
curl -H "traceparent: 00-$(openssl rand -hex 16)-$(openssl rand -hex 8)-01" https://grafana.<domain>/api/health
```

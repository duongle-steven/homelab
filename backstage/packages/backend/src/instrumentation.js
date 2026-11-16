// OpenTelemetry for the backend (loaded with node --require before anything else): traces over OTLP/HTTP to the
// gateway Collector (system/opentelemetry), at OTEL_EXPORTER_OTLP_ENDPOINT; nothing is exported when it is unset
const { NodeSDK } = require('@opentelemetry/sdk-node');
const {
  getNodeAutoInstrumentations,
} = require('@opentelemetry/auto-instrumentations-node');
const {
  OTLPTraceExporter,
} = require('@opentelemetry/exporter-trace-otlp-http');

if (process.env.OTEL_EXPORTER_OTLP_ENDPOINT) {
  const sdk = new NodeSDK({
    serviceName: 'backstage',
    traceExporter: new OTLPTraceExporter(),
    instrumentations: [
      getNodeAutoInstrumentations({
        // Noisy and of no use: every file read
        '@opentelemetry/instrumentation-fs': { enabled: false },
      }),
    ],
  });
  sdk.start();
}

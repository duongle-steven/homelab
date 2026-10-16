{{/* The node network: global.addresses.router masked to global.addresses.prefixLength (192.168.0.1, 24 ->
192.168.0.0/24); Kubernetes warns on a CIDR with host bits set */}}
{{- define "egress.nodeNetwork" -}}
{{- $router := required "global.addresses.router (infra/<env>/cluster/config.json network.gateway) is required" .Values.global.addresses.router -}}
{{- $prefix := int .Values.global.addresses.prefixLength -}}
{{- if or (lt $prefix 1) (gt $prefix 32) -}}
{{- fail (printf "global.addresses.prefixLength (network.prefix_length) must be 1-32, got %v" .Values.global.addresses.prefixLength) -}}
{{- end -}}
{{- $octets := list -}}
{{- range $i, $octet := splitList "." $router -}}
{{- $bits := max 0 (min 8 (sub $prefix (mul 8 $i))) -}}
{{- $block := index (list 256 128 64 32 16 8 4 2 1) (int $bits) -}}
{{- $octets = append $octets (sub (atoi $octet) (mod (atoi $octet) $block)) -}}
{{- end -}}
{{ join "." $octets }}/{{ $prefix }}
{{- end }}

{{/* Ports of a table entry list: {} is every TCP and UDP port, no port is every port of the protocol */}}
{{- define "egress.ports" -}}
{{- range . }}
{{- if and (not .port) (not .protocol) }}
- protocol: TCP
- protocol: UDP
{{- else }}
- protocol: {{ .protocol | default "TCP" }}
  {{- with .port }}
  port: {{ . }}
  {{- end }}
{{- end }}
{{- end }}
{{- end }}

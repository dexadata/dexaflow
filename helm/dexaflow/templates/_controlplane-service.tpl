{{/*
Control-plane Service (ADR 0049), parameterized by role. Takes a dict {ctx, role}.
The "all" role reproduces the pre-split Service byte-for-byte (bare fullname,
selector without a component label, http+metrics+grpc). The split roles each
expose only the ports their role serves and select only their own pods:
  - api:       http + metrics   (the user-facing API + UI; ingress targets this)
  - scheduler: grpc + metrics   (task pods dial grpc here to report state)
metrics is exposed in every role so Prometheus can scrape each Deployment.
*/}}
{{- define "leoflow.controlPlaneService" -}}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "leoflow.roleName" (dict "ctx" .ctx "role" .role) }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "leoflow.labels" .ctx | nindent 4 }}
  {{- with .ctx.Values.service.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  type: {{ .ctx.Values.service.type }}
  selector:
    {{- include "leoflow.roleSelectorLabels" (dict "ctx" .ctx "role" .role) | nindent 4 }}
  ports:
    {{- if or (eq .role "all") (eq .role "api") }}
    - name: http
      port: {{ .ctx.Values.ports.http }}
      targetPort: http
      protocol: TCP
    {{- end }}
    - name: metrics
      port: {{ .ctx.Values.ports.metrics }}
      targetPort: metrics
      protocol: TCP
    {{- if or (eq .role "all") (eq .role "scheduler") }}
    - name: grpc
      port: {{ .ctx.Values.ports.grpc }}
      targetPort: grpc
      protocol: TCP
    {{- end }}
{{- end -}}

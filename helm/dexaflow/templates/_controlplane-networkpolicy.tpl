{{- /*
Control-plane NetworkPolicy (DRAFT, Production track), parameterized by role —
ADR 0049. Takes {ctx, role}. The "all" role reproduces the pre-split policy
byte-for-byte. Split roles select only their own pods and open only the ingress
their role serves:
  - api:       http (clients/ingress) + metrics
  - scheduler: grpc (task pods dial back) + metrics
Egress stays permissive-by-default in every role (DNS always; then the operator's
explicit rules, or allow-all) so enabling the policy never silently breaks the
control plane. Real apiserver isolation for the api is enforced by RBAC (the api
SA is unbound — ADR 0049), with this policy as environment-specific defense in
depth. metrics ingress is its own rule, sourced from networkPolicy.metricsFrom.
*/ -}}
{{- define "leoflow.controlPlaneNetworkPolicy" -}}
{{- $ctx := .ctx -}}
{{- $role := .role -}}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "leoflow.roleName" (dict "ctx" $ctx "role" $role) }}
  namespace: {{ $ctx.Release.Namespace }}
  labels:
    {{- include "leoflow.labels" $ctx | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "leoflow.roleSelectorLabels" (dict "ctx" $ctx "role" $role) | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - ports:
        {{- if or (eq $role "all") (eq $role "api") }}
        # HTTP/UI. Restrict sources via networkPolicy.ingressFrom.
        - port: {{ $ctx.Values.ports.http }}
          protocol: TCP
        {{- end }}
        {{- if or (eq $role "all") (eq $role "scheduler") }}
        # Task pods dial the agent gRPC back (ADR 0002/0004).
        - port: {{ $ctx.Values.ports.grpc }}
          protocol: TCP
        {{- end }}
      {{- with $ctx.Values.networkPolicy.ingressFrom }}
      from:
        {{- toYaml . | nindent 8 }}
      {{- end }}
    {{- with $ctx.Values.networkPolicy.metricsFrom }}
    # Metrics scrape (e.g. the Prometheus pods' namespace/labels).
    - ports:
        - port: {{ $ctx.Values.ports.metrics }}
          protocol: TCP
      from:
        {{- toYaml . | nindent 8 }}
    {{- end }}
  egress:
    # DNS is always required for service discovery.
    - ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
    {{- with $ctx.Values.networkPolicy.egress }}
    {{- toYaml . | nindent 4 }}
    {{- else }}
    # Default: allow all other egress (Postgres, Redis, kube-apiserver). Tighten
    # by setting networkPolicy.egress to explicit rules for your data stores + API.
    - {}
    {{- end }}
{{- end -}}

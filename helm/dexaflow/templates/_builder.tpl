{{/*
Helpers of the in-cluster rootless image builder (#1883, path 4 of ADR 0073). Every
builder template goes through these, so the namespace guard and the label set
cannot be left behind by one template that stops calling them.
*/}}

{{/*
Name of the builder's Deployment, ServiceAccount, Role, RoleBinding and
NetworkPolicy: the fullname with a "-builder" suffix, kept within 63 characters
by leoflow.suffixRole.
*/}}
{{- define "leoflow.builderName" -}}
{{- include "leoflow.suffixRole" (dict "base" (include "leoflow.fullname" .) "role" "builder") -}}
{{- end -}}

{{/*
Pod labels of the builder. The application name is the chart name with a
"-builder" suffix, not leoflow.name, for the reason leoflow.migrateName gives:
a set that carries the control-plane pair (name and instance) is selected by any
control-plane Service or PodDisruptionBudget that shares its namespace, and a
component label added next to that pair does not prevent it.
*/}}
{{- define "leoflow.builderSelectorLabels" -}}
app.kubernetes.io/name: {{ include "leoflow.suffixRole" (dict "base" (include "leoflow.name" .) "role" "builder") }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: builder
{{- end -}}

{{/*
Metadata labels of the builder's objects.
*/}}
{{- define "leoflow.builderLabels" -}}
helm.sh/chart: {{ include "leoflow.chart" . }}
{{ include "leoflow.builderSelectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
The builder namespace, validated. The builder runs build steps from
Dockerfiles the people who deploy write, under a seccomp and AppArmor posture the
restricted and baseline Pod Security levels refuse, so it gets a namespace of
its own: never the release namespace (the control plane and its secrets) and
never the task namespace (task pods and their ServiceAccounts). The chart does
not create the namespace, as it does not create the task namespace.
*/}}
{{- define "leoflow.builderNamespace" -}}
{{- $ns := .Values.builder.namespace | default "" | toString | trim -}}
{{- if not $ns -}}
{{- fail "builder.namespace is required when builder.enabled is true: the builder runs in a namespace of its own, which the chart does not create. Create it (for example `kubectl create namespace dexaflow-builder`, labeled for the privileged Pod Security level unless builder.seccompProfile and builder.appArmorProfile name Localhost profiles) and set builder.namespace to it. See #1883." -}}
{{- end -}}
{{- if eq $ns .Release.Namespace -}}
{{- fail (printf "builder.namespace must not be the release namespace (%s): build steps run code from the Dockerfiles of the people who deploy, so the builder is kept away from the control plane and its secrets. Create a namespace for it and set builder.namespace. See #1883." $ns) -}}
{{- end -}}
{{- if eq $ns (include "leoflow.taskNamespace" .) -}}
{{- fail (printf "builder.namespace must not be the task namespace (%s): the builder runs under a looser Pod Security posture than task pods, and sharing the namespace would loosen it for them too. Create a namespace for it and set builder.namespace. See #1883." $ns) -}}
{{- end -}}
{{- $ns -}}
{{- end -}}

{{/*
A seccomp or AppArmor profile of the builder container, validated. Takes
{value, key, types}. Empty renders nothing; otherwise the type must be one the
field accepts, and a Localhost type must name its profile.
*/}}
{{- define "leoflow.builderProfile" -}}
{{- $p := .value | default dict -}}
{{- if $p -}}
{{- if not (kindIs "map" $p) -}}
{{- fail (printf "%s must be a map such as {type: Unconfined} (got %v)" .key $p) -}}
{{- end -}}
{{- $type := $p.type | default "" | toString -}}
{{- if not (has $type .types) -}}
{{- fail (printf "%s.type=%q is not one of %s" .key $type (join ", " .types)) -}}
{{- end -}}
{{- if and (eq $type "Localhost") (not $p.localhostProfile) -}}
{{- fail (printf "%s.localhostProfile is required when %s.type is Localhost" .key .key) -}}
{{- end -}}
{{- toYaml $p -}}
{{- end -}}
{{- end -}}

{{/*
The subjects of the build RoleBinding, validated: each a User or a Group (in the
RBAC API group) or a ServiceAccount (with its namespace).
*/}}
{{- define "leoflow.builderSubjects" -}}
{{- $out := list -}}
{{- range $i, $s := (.Values.builder.subjects | default list) -}}
{{- if not (kindIs "map" $s) -}}
{{- fail (printf "builder.subjects[%d] must be a map with kind and name" $i) -}}
{{- end -}}
{{- $kind := $s.kind | default "" | toString -}}
{{- if not $s.name -}}
{{- fail (printf "builder.subjects[%d] has no name" $i) -}}
{{- end -}}
{{- if has $kind (list "User" "Group") -}}
{{- $out = append $out (dict "kind" $kind "apiGroup" "rbac.authorization.k8s.io" "name" (toString $s.name)) -}}
{{- else if eq $kind "ServiceAccount" -}}
{{- if not $s.namespace -}}
{{- fail (printf "builder.subjects[%d] is a ServiceAccount and needs its namespace" $i) -}}
{{- end -}}
{{- $out = append $out (dict "kind" $kind "name" (toString $s.name) "namespace" (toString $s.namespace)) -}}
{{- else -}}
{{- fail (printf "builder.subjects[%d].kind=%q is not User, Group or ServiceAccount" $i $kind) -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

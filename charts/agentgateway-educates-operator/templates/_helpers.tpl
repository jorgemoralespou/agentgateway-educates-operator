{{/*
Chart name, overridable.
*/}}
{{- define "agentgateway-educates-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "agentgateway-educates-operator.fullname" -}}
{{- default .Chart.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels. Deliberately version-free in the selector below, so an upgrade
does not orphan the previous ReplicaSet.
*/}}
{{- define "agentgateway-educates-operator.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "agentgateway-educates-operator.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "agentgateway-educates-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agentgateway-educates-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Resolve the image registry: values first, then global, then the Chart.yaml
annotations. Fails loudly rather than producing an image reference with an empty
registry, which would silently resolve to Docker Hub.
*/}}
{{- define "agentgateway-educates-operator.imageRegistryPrefix" -}}
{{- $reg := "" -}}
{{- if .Values.development.imageRegistry -}}
{{- $reg = printf "%s/%s" .Values.development.imageRegistry.host .Values.development.imageRegistry.namespace -}}
{{- else if and .Values.global .Values.global.development .Values.global.development.imageRegistry -}}
{{- $reg = printf "%s/%s" .Values.global.development.imageRegistry.host .Values.global.development.imageRegistry.namespace -}}
{{- else -}}
{{- $host := index .Chart.Annotations "educates.dev/image-registry-host" -}}
{{- $ns := index .Chart.Annotations "educates.dev/image-registry-namespace" -}}
{{- if or (not $host) (not $ns) -}}
{{- fail "cannot resolve an image registry: set image.repository, development.imageRegistry, or the chart's registry annotations" -}}
{{- end -}}
{{- $reg = printf "%s/%s" $host $ns -}}
{{- end -}}
{{- $reg -}}
{{- end -}}

{{- define "agentgateway-educates-operator.image.repository" -}}
{{- if .Values.image.repository -}}
{{- .Values.image.repository -}}
{{- else -}}
{{- printf "%s/%s" (include "agentgateway-educates-operator.imageRegistryPrefix" .) .Chart.Name -}}
{{- end -}}
{{- end -}}

{{- define "agentgateway-educates-operator.image.tag" -}}
{{- default .Chart.AppVersion .Values.image.tag -}}
{{- end -}}

{{/*
Derive a pull policy when none is set: Always for a floating tag, so a
development build is picked up, IfNotPresent otherwise.
*/}}
{{- define "agentgateway-educates-operator.image.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else -}}
{{- $tag := include "agentgateway-educates-operator.image.tag" . -}}
{{- if or (eq $tag "latest") (hasSuffix "-dev" $tag) (hasPrefix "main" $tag) -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}
{{- end -}}

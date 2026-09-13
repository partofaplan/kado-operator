{{/* Expand the name of the chart. */}}
{{- define "kado-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, capped at 63 chars for the DNS label limit.
*/}}
{{- define "kado-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "kado-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kado-operator.labels" -}}
helm.sh/chart: {{ include "kado-operator.chart" . }}
{{ include "kado-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kado-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kado-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kado-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kado-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "kado-operator.imageTag" -}}
{{- default .Chart.AppVersion .Values.image.tag }}
{{- end }}

{{- define "kado-operator.image" -}}
{{- printf "%s:%s" .Values.image.repository (include "kado-operator.imageTag" .) }}
{{- end }}

{{/*
Pull policy. An explicit image.pullPolicy always wins; empty means "decide from
the tag", which is what Kubernetes itself does for a bare pod spec.

This matters because the chart's own appVersion is "latest" (#18). A moving tag
with IfNotPresent means a node that already cached an older "latest" keeps
running it, and `helm upgrade` reports success while changing nothing. Always
for a moving tag, IfNotPresent for an immutable one — the same rule deploy.yml
applies to the dispatched path.
*/}}
{{- define "kado-operator.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if eq (include "kado-operator.imageTag" .) "latest" -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end }}

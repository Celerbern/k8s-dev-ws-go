{{/*
Expand the name of the chart.
*/}}
{{- define "devws.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "devws.labels" -}}
app.kubernetes.io/name: {{ include "devws.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: devws
{{- end }}

{{/*
The control plane image reference.
*/}}
{{- define "devws.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{ .Values.image.repository }}:{{ $tag }}
{{- end }}

{{/*
The platform namespace (the release namespace).
*/}}
{{- define "devws.namespace" -}}
{{ .Release.Namespace }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "devws.selectorLabels" -}}
app.kubernetes.io/name: {{ include "devws.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "devws.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (printf "%s-apiserver" (include "devws.name" .)) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end }}
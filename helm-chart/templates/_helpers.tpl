{{/*
Expand the name of the chart.
*/}}
{{- define "hosting-panel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "hosting-panel.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "hosting-panel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "hosting-panel.labels" -}}
helm.sh/chart: {{ include "hosting-panel.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: hosting-panel
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{/*
Panel Core labels
*/}}
{{- define "hosting-panel.panelLabels" -}}
{{ include "hosting-panel.labels" . }}
app.kubernetes.io/name: panel-core
app.kubernetes.io/component: panel
{{- end }}

{{/*
Operator labels
*/}}
{{- define "hosting-panel.operatorLabels" -}}
{{ include "hosting-panel.labels" . }}
app.kubernetes.io/name: hosting-operator
app.kubernetes.io/component: operator
{{- end }}

{{/*
Panel selector labels
*/}}
{{- define "hosting-panel.panelSelectorLabels" -}}
app.kubernetes.io/name: panel-core
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Operator selector labels
*/}}
{{- define "hosting-panel.operatorSelectorLabels" -}}
app.kubernetes.io/name: hosting-operator
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Namespace for hosting system
*/}}
{{- define "hosting-panel.namespace" -}}
{{- default "hosting-system" .Values.global.namespace }}
{{- end }}

{{/*
Longhorn replica count based on node count
*/}}
{{- define "hosting-panel.replicaCount" -}}
{{- if eq (int .Values.global.nodeCount) 1 }}1{{- else }}3{{- end }}
{{- end }}

{{/*
Keycloak internal URL
*/}}
{{- define "hosting-panel.keycloakUrl" -}}
http://{{ .Release.Name }}-keycloak.{{ include "hosting-panel.namespace" . }}.svc.cluster.local
{{- end }}

{{/*
Galera internal host
*/}}
{{- define "hosting-panel.galeraHost" -}}
{{ .Release.Name }}-mariadb-galera.{{ include "hosting-panel.namespace" . }}.svc.cluster.local
{{- end }}

{{/*
PowerDNS API internal URL
*/}}
{{- define "hosting-panel.powerdnsApiUrl" -}}
http://{{ include "hosting-panel.fullname" . }}-powerdns.{{ include "hosting-panel.namespace" . }}.svc.cluster.local:{{ .Values.powerdns.api.port }}
{{- end }}

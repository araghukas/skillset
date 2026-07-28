{{- define "skillsd.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "skillsd.fullname" -}}
{{- if .Release.Name | eq .Chart.Name -}}
{{- .Chart.Name -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "skillsd.labels" -}}
app.kubernetes.io/name: {{ include "skillsd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "skillsd.selectorLabels" -}}
app.kubernetes.io/name: {{ include "skillsd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "skillsd.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "skillsd.fullname" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

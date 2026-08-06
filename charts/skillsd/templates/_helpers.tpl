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

{{- define "skillsd.registryFullname" -}}
{{- printf "%s-registry" (include "skillsd.fullname" .) -}}
{{- end -}}

{{/* Namespace every object in this chart is created in. */}}
{{- define "skillsd.namespace" -}}
{{- .Values.namespace | default .Release.Namespace -}}
{{- end -}}

{{/* Path the GitHub App private key Secret is mounted at, and the key within it. */}}
{{- define "skillsd.githubAppKeyPath" -}}/etc/github-app/private-key.pem{{- end -}}
{{- define "skillsd.githubAppKeyName" -}}private-key.pem{{- end -}}

{{- define "skillsd.githubAuthMode" -}}
{{- $auth := .auth -}}
{{- $app := $auth.githubApp | default dict -}}
{{- $set := list -}}
{{- if $app.appId }}{{- $set = append $set "appId" }}{{- end -}}
{{- if $app.installationId }}{{- $set = append $set "installationId" }}{{- end -}}
{{- if $app.privateKeySecret }}{{- $set = append $set "privateKeySecret" }}{{- end -}}
{{- if eq (len $set) 3 -}}
{{- if $auth.tokenSecret -}}
{{- fail (printf "%s: set either tokenSecret or githubApp, not both" .label) -}}
{{- end -}}
githubApp
{{- else if gt (len $set) 0 -}}
{{- fail (printf "%s.githubApp is incomplete: appId, installationId, and privateKeySecret are all required (got: %s)" .label (join ", " $set)) -}}
{{- else if $auth.tokenSecret -}}
token
{{- else -}}
none
{{- end -}}
{{- end -}}

{{- define "skillsd.githubAuthEnv" -}}
{{- $mode := include "skillsd.githubAuthMode" . -}}
- name: GITHUB_AUTH_MODE
  value: {{ $mode | quote }}
{{- if eq $mode "token" }}
- name: GITHUB_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ .auth.tokenSecret }}
      key: token
{{- else if eq $mode "githubApp" }}
- name: GITHUB_APP_ID
  value: {{ .auth.githubApp.appId | quote }}
- name: GITHUB_APP_INSTALLATION_ID
  value: {{ .auth.githubApp.installationId | quote }}
- name: GITHUB_APP_PRIVATE_KEY_PATH
  value: {{ include "skillsd.githubAppKeyPath" . | quote }}
{{- end }}
{{- end -}}

{{- define "skillsd.githubAuthVolume" -}}
{{- if eq (include "skillsd.githubAuthMode" .) "githubApp" }}
- name: github-app-key
  secret:
    secretName: {{ .auth.githubApp.privateKeySecret }}
    defaultMode: 0440
    items:
      - key: {{ include "skillsd.githubAppKeyName" . }}
        path: {{ include "skillsd.githubAppKeyName" . }}
{{- end }}
{{- end -}}

{{- define "skillsd.githubAuthVolumeMount" -}}
{{- if eq (include "skillsd.githubAuthMode" .) "githubApp" }}
- name: github-app-key
  mountPath: {{ include "skillsd.githubAppKeyPath" . | dir }}
  readOnly: true
{{- end }}
{{- end -}}

{{- define "skillsd.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "skillsd.fullname" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

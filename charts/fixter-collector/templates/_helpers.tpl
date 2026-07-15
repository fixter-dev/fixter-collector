{{- define "fixter-collector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "fixter-collector.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "fixter-collector.labels" -}}
app.kubernetes.io/name: {{ include "fixter-collector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{- define "fixter-collector.serviceAccountName" -}}
{{- include "fixter-collector.fullname" . -}}
{{- end -}}

{{- define "fixter-collector.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- define "fixter-collector.secretName" -}}
{{- if .Values.fixter.existingSecret -}}
{{- .Values.fixter.existingSecret -}}
{{- else -}}
{{- include "fixter-collector.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "fixter-collector.secretKey" -}}
{{- if .Values.fixter.existingSecret -}}
{{- .Values.fixter.existingSecretKey -}}
{{- else -}}
FIXTER_API_KEY
{{- end -}}
{{- end -}}

{{- define "fixter-collector.validate" -}}
{{- if and .Values.fixter.apiKey .Values.fixter.existingSecret -}}
{{- fail "set fixter.apiKey OR fixter.existingSecret, not both" -}}
{{- end -}}
{{- if not (or .Values.fixter.apiKey .Values.fixter.existingSecret) -}}
{{- fail "no Fixter credentials: set fixter.apiKey (or fixter.existingSecret). Get a key at https://fixter.dev -> Settings -> API Keys" -}}
{{- end -}}
{{- if not .Values.fixter.endpoint -}}
{{- fail "fixter.endpoint must not be empty" -}}
{{- end -}}
{{- end -}}

{{/* GOMEMLIMIT from a Kubernetes memory quantity.

     Kubernetes and Go do NOT share a format: k8s says `512Mi`, Go demands
     `512MiB`. Feeding a k8s quantity straight to GOMEMLIMIT makes the Go
     runtime abort during init — before main(), before --config is read — so
     the pod crash-loops with `malformed GOMEMLIMIT` and no collector log.
     Verified against the real image.

     Go accepts only binary suffixes (B/KiB/MiB/GiB/TiB), so a decimal k8s
     quantity like `512M` cannot be converted by appending B. Reject it at
     template time rather than shipping a crash-loop. */}}
{{- define "fixter-collector.goMemLimit" -}}
{{- $m := . | toString -}}
{{- if not (regexMatch "^[0-9]+(Ki|Mi|Gi|Ti)$" $m) -}}
{{- fail (printf "resources.limits.memory must use a binary suffix (Ki/Mi/Gi/Ti), got %q. Go's GOMEMLIMIT cannot parse decimal suffixes and the collector would crash-loop." $m) -}}
{{- end -}}
{{- printf "%sB" $m -}}
{{- end -}}

{{/* Shared resource attributes. k8s.cluster.name is emitted only when set —
     it is optional, and simply absent otherwise. See Global Constraints for
     why cloud auto-detection is not used. */}}
{{- define "fixter-collector.resourceAttributes" -}}
- key: deployment.environment
  value: {{ .Values.fixter.environment | quote }}
  action: upsert
{{- if .Values.fixter.clusterName }}
- key: k8s.cluster.name
  value: {{ .Values.fixter.clusterName | quote }}
  action: upsert
{{- end }}
{{- end -}}

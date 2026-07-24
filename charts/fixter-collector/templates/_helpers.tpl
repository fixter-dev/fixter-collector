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
{{- fail "no Fixter credentials: set fixter.apiKey (or fixter.existingSecret). Get a key at https://app.fixter.dev -> Settings -> API Keys" -}}
{{- end -}}
{{- if not .Values.fixter.endpoint -}}
{{- fail "fixter.endpoint must not be empty" -}}
{{- end -}}
{{- include "fixter-collector.rejectLegacyParsing" . -}}
{{- end -}}

{{- define "fixter-collector.rejectLegacyParsing" -}}
{{- if hasKey .Values.agent.logs "parsing" -}}
{{- fail "agent.logs.parsing was removed in chart 0.2.0. Helm ignores unknown keys silently, so leaving it there would drop your overrides without a word and cost you the severities you believe you are setting. Migrate it: agent.logs.parsing.json -> agent.logs.structural.json, and agent.logs.parsing.glog -> agent.logs.structural.glog (behaviour unchanged, both on by default). agent.logs.parsing.text and agent.logs.parsing.continuationRegex are gone entirely — they applied one universal parser to every pod, which mislabelled severity (an ERROR from a service named `trace-service` arrived as Trace(1)). Add an agent.logs.formats entry with the matching preset for each text-logging workload instead. See the 0.1.x -> 0.2.0 migration note in the README." -}}
{{- end -}}
{{- end -}}

{{- define "fixter-collector.goMemLimit" -}}
{{- $m := . | toString -}}
{{- if not (regexMatch "^[0-9]+(Ki|Mi|Gi|Ti)$" $m) -}}
{{- fail (printf "resources.limits.memory must use a binary suffix (Ki/Mi/Gi/Ti), got %q. Go's GOMEMLIMIT cannot parse decimal suffixes and the collector would crash-loop." $m) -}}
{{- end -}}
{{- printf "%sB" $m -}}
{{- end -}}

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

{{- define "fixter-collector.baseLogExcludes" -}}
- /var/log/pods/*_{{ include "fixter-collector.fullname" . }}*_*/*/*.log
{{- range .Values.agent.logs.excludeNamespaces }}
- /var/log/pods/{{ . }}_*/*/*.log
{{- end }}
{{- range .Values.agent.logs.excludePods }}
- /var/log/pods/*_{{ . }}*_*/*/*.log
{{- end }}
{{- end -}}

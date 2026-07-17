{{- define "fixter-collector.structuralSeverity" -}}
{{- $s := .root.Values.agent.logs.structural -}}
{{- if or $s.json.enabled $s.glog.enabled }}
- type: router
  id: format-router
  routes:
    {{- if $s.json.enabled }}
    - expr: 'body matches "^\\s*\\{"'
      output: json-severity
    {{- end }}
    {{- if $s.glog.enabled }}
    - expr: {{ printf "body matches %s" ($s.glog.detectRegex | toJson) | quote }}
      output: glog-severity
    {{- end }}
  default: parsed
{{- if $s.json.enabled }}
- type: json_parser
  id: json-severity
  parse_from: body
  parse_to: attributes
  on_error: send_quiet
  output: parsed
  severity:
    parse_from: attributes.{{ $s.json.severityField }}
    {{- with $s.json.severityMapping }}
    mapping:
      {{- range $level, $value := . }}
      {{ $level }}: {{ $value | toJson }}
      {{- end }}
    {{- end }}
  trace:
    trace_id:
      parse_from: attributes.{{ $s.json.traceIdField }}
    span_id:
      parse_from: attributes.{{ $s.json.spanIdField }}
    trace_flags:
      parse_from: attributes.{{ $s.json.traceFlagsField }}
{{- end }}
{{- if $s.glog.enabled }}
- type: regex_parser
  id: glog-severity
  parse_from: body
  regex: {{ $s.glog.severityRegex | quote }}
  parse_to: attributes
  on_error: send_quiet
  output: parsed
  severity:
    parse_from: attributes.severity
    {{- with $s.glog.severityMapping }}
    mapping:
      {{- range $level, $values := . }}
      {{ $level }}: {{ $values | toJson }}
      {{- end }}
    {{- end }}
{{- end }}
- type: noop
  id: parsed
{{- end }}
{{- end -}}

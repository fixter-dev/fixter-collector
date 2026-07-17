{{- define "fixter-collector.logOperators" -}}
{{- $spec := .spec -}}
{{- if and $spec.firstEntryRegex $spec.continuationRegex -}}
{{- fail "a log format sets both firstEntryRegex and continuationRegex; they are two directions of the same predicate and recombine takes only one. firstEntryRegex wins outright, so the continuationRegex beside it is dead config that silently does nothing — and a rule you believe is joining your stack traces, but is not, is exactly the failure this chart fails the template to prevent. Set exactly one. If you set firstEntryRegex on a format whose preset ships a continuationRegex, also set continuationRegex to null to drop it — but note firstEntryRegex is the UNSAFE direction: a line it does not match is appended to the record above, so a pattern that misses your format merges the entire stream into one record and stamps it with the first line's severity. A too-narrow continuationRegex fragments instead, which is recoverable — but a too-BROAD one merges just as badly, so bound whichever you set. See agent.logs.presets in values.yaml." -}}
{{- end -}}
- type: container
  id: container-parser
{{- if or $spec.firstEntryRegex $spec.continuationRegex }}
- type: add
  id: recombine-key
  field: attributes.recombine_source
  value: 'EXPR(attributes["log.file.path"] + "|" + attributes["log.iostream"])'
- type: recombine
  id: multiline
  combine_field: body
  combine_with: "\n"
  {{- if $spec.firstEntryRegex }}
  is_first_entry: {{ printf "body matches %s" ($spec.firstEntryRegex | toJson) | quote }}
  {{- else }}
  is_first_entry: {{ printf "!(body matches %s)" ($spec.continuationRegex | toJson) | quote }}
  {{- end }}
  source_identifier: attributes["recombine_source"]
  max_log_size: {{ $spec.maxLogSize | default "1MiB" }}
  force_flush_period: {{ $spec.forceFlushPeriod | default "5s" }}
{{- end }}
{{- if and $spec.severityKey $spec.severityRegex }}
{{- fail "a log format sets both severityKey and severityRegex; they are two different parsers for the same field and only one can run. severityKey parses the line as key=value pairs (key_value_parser); severityRegex pattern-matches it (regex_parser). Set exactly one. If you set severityKey on a format whose preset ships a severityRegex (or vice versa), also set the other to null to drop it." }}
{{- end }}
{{- if or $spec.severityKey $spec.severityRegex }}
{{- if $spec.severityKey }}
- type: key_value_parser
  id: format-severity
  parse_from: body
  parse_to: attributes
  on_error: send_quiet
  severity:
    parse_from: attributes.{{ $spec.severityKey }}
{{- else }}
- type: regex_parser
  id: format-severity
  parse_from: body
  regex: {{ $spec.severityRegex | quote }}
  parse_to: attributes
  on_error: send_quiet
  severity:
    parse_from: attributes.severity
{{- end }}
    {{- with $spec.severityMapping }}
    mapping:
      {{- range $level, $values := . }}
      {{ $level }}: {{ $values | toJson }}
      {{- end }}
    {{- end }}
{{- end }}
{{- end -}}

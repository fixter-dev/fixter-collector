{{- define "fixter-collector.validateLogFormats" -}}
{{- $seen := dict -}}
{{- $seenIncludes := dict -}}
{{- range $i, $fmt := . -}}
{{- $name := $fmt.name | default $fmt.preset -}}
{{- if not $name -}}
{{- fail (printf "agent.logs.formats[%d] has no name. Set name, or set preset to take the name from the preset." $i) -}}
{{- end -}}
{{- if hasKey $seen $name -}}
{{- fail (printf "duplicate agent.logs.formats name %q. Each name becomes a file_log/<name> receiver key, so duplicates render the same YAML key twice and the collector crash-loops with `mapping key \"file_log/%s\" already defined`. Give each format a unique name." $name $name) -}}
{{- end -}}
{{- $_ := set $seen $name true -}}
{{- if not $fmt.include -}}
{{- fail (printf "agent.logs.formats %q has no include globs. Every format needs its own `include`: a preset supplies the parser, never the location. Without it the receiver renders `include: null` and the collector refuses to start with `failed to create \"file_log/%s\" receiver ... 'include' must be specified` — the agent DaemonSet crash-loops and the node ships no logs at all, for any workload. Set include to the log paths this format claims, e.g. include: [\"/var/log/pods/<namespace>_<pod>-*/*/*.log\"]." $name $name) -}}
{{- end -}}
{{- $key := $fmt.include | sortAlpha | toJson -}}
{{- if hasKey $seenIncludes $key -}}
{{- fail (printf "agent.logs.formats %q and %q have identical include globs %s. Earlier formats take precedence, so %q is excluded from every file it asks for and can never collect anything — its operators would never run. Remove %q, or narrow its globs so it claims files %q does not." (get $seenIncludes $key) $name $key $name $name (get $seenIncludes $key)) -}}
{{- end -}}
{{- $_ := set $seenIncludes $key $name -}}
{{- end -}}
{{- end -}}

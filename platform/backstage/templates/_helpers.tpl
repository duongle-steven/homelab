{{/* Kanidm's LDAP base DN: its domain's labels (auth.<domain>) */}}
{{- define "backstage.ldapBaseDN" -}}
{{- $labels := list -}}
{{- range splitList "." (printf "auth.%s" .Values.global.baseDomain) -}}
{{- $labels = append $labels (printf "dc=%s" .) -}}
{{- end -}}
{{- join "," $labels -}}
{{- end -}}

{{/* A file of the repository Argo CD syncs (global.gitRepository at global.gitRevision), as Backstage's URL reader wants
it: GitHub's blob URL, or Forgejo's (Gitea's) src/branch URL */}}
{{- define "backstage.location" -}}
{{- $g := .root.Values.global -}}
{{- if contains "github.com" $g.gitRepository -}}
{{ trimSuffix ".git" $g.gitRepository }}/blob/{{ $g.gitRevision }}/{{ .path }}
{{- else -}}
{{ trimSuffix ".git" $g.gitRepository }}/src/branch/{{ $g.gitRevision }}/{{ .path }}
{{- end -}}
{{- end -}}

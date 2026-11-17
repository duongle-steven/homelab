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

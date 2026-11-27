{{/* Labels on every object. */}}
{{- define "continuum.labels" -}}
app.kubernetes.io/name: continuum
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{/*
continuum.image renders one continuum image reference from
(dict "root" $ "image" .Values.images.<name>): a digest pins it; otherwise
the tag names it.
*/}}
{{- define "continuum.image" -}}
{{- $img := .image -}}
{{- $reg := .root.Values.images.registry -}}
{{- $ref := ternary (printf "%s/%s" $reg $img.repository) $img.repository (ne $reg "") -}}
{{- if $img.digest -}}
{{- printf "%s@%s" $ref $img.digest -}}
{{- else if $img.tag -}}
{{- printf "%s:%s" $ref $img.tag -}}
{{- else -}}
{{- fail (printf "image %s needs a digest or a tag" $img.repository) -}}
{{- end -}}
{{- end }}

{{- define "continuum.pullSecrets" -}}
{{- with .Values.images.pullSecrets }}
imagePullSecrets:
{{- range . }}
  - name: {{ . }}
{{- end }}
{{- end }}
{{- end }}

{{/* The genesis pool count: every pool of either implementation. */}}
{{- define "continuum.totalPools" -}}
{{- add .Values.pools.cardanoNode .Values.pools.dingo -}}
{{- end }}

{{/* The Secret holding the run seed under key "seed". */}}
{{- define "continuum.seedSecret" -}}
{{- default "continuum-seed" .Values.seed.existingSecret -}}
{{- end }}

{{/*
continuum.poolName is the operator's name for forging pool i's workload,
"pool-" and the first 16 hex characters of SHA-256 of its poolId
(render.Name and render.PoolKey). It is the Service the engine dials.
*/}}
{{- define "continuum.poolName" -}}
{{- printf "pool-%s" (printf "continuum-pool-%d" (int .) | sha256sum | trunc 16) -}}
{{- end }}

{{- define "continuum.check" -}}
{{- $total := int (include "continuum.totalPools" .) -}}
{{- if lt (int .Values.pools.cardanoNode) 1 -}}
{{- fail "pools.cardanoNode must be at least 1: the engine follows pool 0, a cardano-node pool" -}}
{{- end -}}
{{- if gt $total (int .Values.network.budget.maxPools) -}}
{{- fail (printf "%d pools exceed network.budget.maxPools %d" $total (int .Values.network.budget.maxPools)) -}}
{{- end -}}
{{- if and (has "exunits-block-gap" .Values.engine.attacks) (not .Values.genesis.allowInconsistentExUnits) -}}
{{- fail "engine.attacks names exunits-block-gap: set genesis.overrides and genesis.allowInconsistentExUnits (values.yaml)" -}}
{{- end -}}
{{- end }}

{{- define "zeus.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else -}}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "zeus.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* zeus.selector: (dict "root" $ "component" "api") */}}
{{- define "zeus.selector" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "zeus.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ default (include "zeus.fullname" .) .Values.serviceAccount.name }}
{{- else -}}{{ default "default" .Values.serviceAccount.name }}{{- end -}}
{{- end -}}

{{/* zeus.image: (dict "root" $ "image" .Values.image.server) */}}
{{- define "zeus.image" -}}
{{- $v := .root.Values.image -}}
{{- $tag := .image.tag | default $v.tag | default .root.Chart.AppVersion -}}
{{- if $v.registry -}}{{ printf "%s/%s:%s" (trimSuffix "/" $v.registry) .image.repository $tag }}
{{- else -}}{{ printf "%s:%s" .image.repository $tag }}{{- end -}}
{{- end -}}

{{/* Environment shared by every process; extra is a map of per-process overrides. */}}
{{- define "zeus.env" -}}
{{- $v := .root.Values -}}
{{- $env := dict "ZEUS_ENV" $v.env "ZEUS_REGION" $v.region "ZEUS_PUBLIC_BASE_URL" $v.publicBaseURL -}}
{{- if $v.modelgw }}{{ $_ := set $env "ZEUS_MODELGW_CONFIG" "/etc/zeus/modelgw.yaml" }}{{ end -}}
{{- if $v.moderationKeywords }}{{ $_ := set $env "ZEUS_MODERATION_KEYWORDS_FILE" "/etc/zeus/moderation-keywords.txt" }}{{ end -}}
{{- if $v.blob.enabled }}{{ $_ := set $env "ZEUS_BLOB_DIR" "/data/blob" }}{{ end -}}
{{- if $v.apnsKeySecret }}{{ $_ := set $env "ZEUS_APNS_KEY_FILE" "/etc/zeus/apns/apns.p8" }}{{ end -}}
{{- $env = merge (deepCopy (.extra | default dict)) (deepCopy $v.config) $env -}}
{{- range $k := keys $env | sortAlpha }}
- name: {{ $k }}
  value: {{ get $env $k | toString | quote }}
{{- end }}
{{- end -}}

{{- define "zeus.envFrom" -}}
{{- if .Values.existingSecret }}
envFrom:
  - secretRef:
      name: {{ .Values.existingSecret }}
{{- end }}
{{- end -}}

{{- define "zeus.volumeMounts" -}}
- name: files
  mountPath: /etc/zeus/modelgw.yaml
  subPath: modelgw.yaml
- name: files
  mountPath: /etc/zeus/moderation-keywords.txt
  subPath: moderation-keywords.txt
{{- if .Values.blob.enabled }}
- name: blob
  mountPath: /data/blob
{{- end }}
{{- if .Values.apnsKeySecret }}
- name: apns
  mountPath: /etc/zeus/apns
  readOnly: true
{{- end }}
{{- end -}}

{{- define "zeus.volumes" -}}
- name: files
  configMap:
    name: {{ include "zeus.fullname" . }}-files
{{- if .Values.blob.enabled }}
- name: blob
  persistentVolumeClaim:
    claimName: {{ .Values.blob.existingClaim | default (printf "%s-blob" (include "zeus.fullname" .)) }}
{{- end }}
{{- if .Values.apnsKeySecret }}
- name: apns
  secret:
    secretName: {{ .Values.apnsKeySecret }}
{{- end }}
{{- end -}}

{{- define "zeus.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "zeus.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{- define "zeus.scheduling" -}}
{{- with .Values.nodeSelector }}
nodeSelector: {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations: {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity: {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.imagePullSecrets }}
imagePullSecrets: {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* Restart pods when the mounted files change. */}}
{{- define "zeus.podAnnotations" -}}
checksum/files: {{ include (print .Template.BasePath "/configmap.yaml") . | sha256sum }}
{{- with .Values.podAnnotations }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* One zeusctl container for the migrate Job: (dict "root" $ "cmd" "migrate") */}}{{- define "zeus.zeusctl" -}}
- name: {{ .cmd }}
  image: {{ include "zeus.image" (dict "root" .root "image" .root.Values.image.server) }}
  imagePullPolicy: {{ .root.Values.image.pullPolicy }}
  command: ["/usr/local/bin/zeusctl", {{ .cmd | quote }}]
  env: {{- include "zeus.env" (dict "root" .root) | nindent 4 }}
  {{- include "zeus.envFrom" .root | nindent 2 }}
  resources: {{- toYaml .root.Values.migrate.resources | nindent 4 }}
  securityContext: {{- include "zeus.containerSecurityContext" .root | nindent 4 }}
{{- end -}}

{{/* zeus.ingress: (dict "root" $ "component" "api" "ingress" .Values.api.ingress "port" 8080) */}}
{{- define "zeus.ingress" -}}
{{- $name := printf "%s-%s" (include "zeus.fullname" .root) .component }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ $name }}
  labels: {{- include "zeus.labels" .root | nindent 4 }}
  {{- with .ingress.annotations }}
  annotations: {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- with .ingress.className }}
  ingressClassName: {{ . }}
  {{- end }}
  {{- if .ingress.tlsSecret }}
  tls:
    - hosts: [{{ .ingress.host | quote }}]
      secretName: {{ .ingress.tlsSecret }}
  {{- end }}
  rules:
    - host: {{ .ingress.host | quote }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ $name }}
                port: { number: {{ .port }} }
{{- end -}}

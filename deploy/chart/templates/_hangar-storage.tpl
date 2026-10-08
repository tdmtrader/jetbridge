{{- define "concourse.hangarStorage.name" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-store") -}}
{{- end }}
{{- define "concourse.hangarStorage.endpoint" -}}
{{- printf "https://%s.%s.svc:7783" (include "concourse.hangarStorage.name" .) .Release.Namespace -}}
{{- end }}
{{- define "concourse.hangarStorage.claim" -}}
{{- default (include "concourse.hangarStorage.name" .) .Values.hangarStorage.disk.existingClaim -}}
{{- end }}
{{/*
The output plane's publisher credential in the artifact daemon Pod: the one
disk-store role the daemon holds, at its own path.
*/}}
{{- define "concourse.hangarStorage.outputClientMount" -}}
- name: hangar-output-disk-client
  mountPath: /etc/concourse/hangar-output-disk
  readOnly: true
{{- end }}
{{- define "concourse.hangarStorage.clientVolume" -}}
- name: {{ .name | default "hangar-disk-client" }}
  projected:
    sources:
      - secret:
          name: {{ .root.Values.hangarStorage.disk.credentials.existingSecret }}
          items:
            - key: {{ .role }}
              path: token
      - secret:
          name: {{ .root.Values.hangarStorage.disk.tls.existingSecret }}
          items:
            - key: ca.crt
              path: ca.crt
{{- end }}

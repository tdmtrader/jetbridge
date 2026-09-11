{{/*
The body every isolated output controller shares: its ServiceAccount, its
Deployment shell, and the pieces that are identical because they are about
ISOLATION rather than about the work.

The FLAGS are deliberately not here. Each controller's own template spells its
own command line, because deploy/chart/tests/flag_drift_test.go reads template
TEXT and asks the real binary what it accepts -- a shared define would put three
binaries' flags in one file that no single binary accepts, and the guard would
either go vacuous or go permanently red. A rendered flag a binary rejects is
every pod CrashLoopBackOffing on the first sync, which has happened here twice.

Expects a dict: root, component, name, account, values.
*/}}
{{- define "concourse.hangarOutput.controllerHead" -}}
{{- $root := .root -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .account }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "concourse.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
  {{- with .values.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .name }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "concourse.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  replicas: {{ int .values.replicas }}
  strategy:
    # Recreate and not RollingUpdate. There is exactly one durable cursor and
    # one renewable lease owner per dedicated output bucket and activation
    # epoch; a rolling update runs the old owner and the new one at once, which
    # is the parallel-owner case the plan forbids by construction rather than
    # mitigates with a lease.
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ include "concourse.name" $root }}
      app.kubernetes.io/component: {{ .component }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{ include "concourse.name" $root }}
        app.kubernetes.io/component: {{ .component }}
    spec:
      serviceAccountName: {{ .account }}
      terminationGracePeriodSeconds: 30
      securityContext:
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: {{ .component }}
          image: {{ include "concourse.image" $root }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
{{- end }}

{{/*
controllerTail is everything after the command line: the database credential
under the activation role's Secret, the resources, and the security context.
No controller mounts the node's hostPath -- the source ledger and the step
incarnations have exactly one writer and it is the daemon.
*/}}
{{- define "concourse.hangarOutput.controllerTail" -}}
{{- $root := .root -}}
          env:
            - name: HANGAR_OUTPUT_DSN
              valueFrom:
                secretKeyRef:
                  name: {{ $root.Values.hangarOutput.database.existingSecret }}
                  key: dsn
          {{- with .values.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- include "concourse.hangarOutput.controllerSecurityContext" $root | nindent 10 }}
{{- end }}

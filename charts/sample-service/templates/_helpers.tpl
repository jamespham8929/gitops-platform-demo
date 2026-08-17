{{/*
Expand the name of the chart.
*/}}
{{- define "sample-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "sample-service.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "sample-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "sample-service.labels" -}}
helm.sh/chart: {{ include "sample-service.chart" . }}
{{ include "sample-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sample-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sample-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the service account to use.
*/}}
{{- define "sample-service.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sample-service.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the service that carries stable traffic. The ingress and the Rollout's
stableService both point here, so it keeps the same name whether the workload is
a Deployment or a Rollout.
*/}}
{{- define "sample-service.stableServiceName" -}}
{{- include "sample-service.fullname" . }}
{{- end }}

{{/*
Name of the canary service. Argo Rollouts rewrites this service's selector to
the canary pod hash during a rollout, which is what lets the analysis query
isolate canary traffic.
*/}}
{{- define "sample-service.canaryServiceName" -}}
{{- printf "%s-canary" (include "sample-service.fullname" .) }}
{{- end }}

{{/*
The pod spec, shared by the Deployment and the Rollout so the two paths cannot
drift apart.
*/}}
{{- define "sample-service.podSpec" -}}
serviceAccountName: {{ include "sample-service.serviceAccountName" . }}
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  seccompProfile:
    type: RuntimeDefault
terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
containers:
  - name: {{ .Chart.Name }}
    image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
    imagePullPolicy: {{ .Values.image.pullPolicy }}
    ports:
      - name: http
        containerPort: 8080
        protocol: TCP
    env:
      - name: PORT
        value: "8080"
      - name: APP_VERSION
        value: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
      - name: FAILURE_RATE
        value: {{ .Values.failureRate | quote }}
      {{- with .Values.env }}
      {{- toYaml . | nindent 6 }}
      {{- end }}
    {{- with .Values.envFrom }}
    envFrom:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    livenessProbe:
      {{- toYaml .Values.livenessProbe | nindent 6 }}
    readinessProbe:
      {{- toYaml .Values.readinessProbe | nindent 6 }}
    resources:
      {{- toYaml .Values.resources | nindent 6 }}
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: [ALL]
{{- end }}

//go:build helm

package opentams_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	chartDir      = "."
	defaultValues = "ci/default-values.yaml"
	releaseName   = "rel"
	fullname      = "rel-opentams"
	gcFullname    = fullname + "-gc"
	chartLabel    = "opentams-0.1.0"
	appVersion    = "v0.1.0"
)

type manifest map[string]any

func requireHelm(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not on PATH")
	}
	return path
}

func render(t *testing.T, setArgs ...string) []manifest {
	t.Helper()
	helm := requireHelm(t)
	args := []string{"template", releaseName, chartDir, "--values", defaultValues}
	for _, s := range setArgs {
		args = append(args, "--set", s)
	}
	cmd := exec.CommandContext(t.Context(), helm, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}

	var docs []manifest
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var doc manifest
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered YAML: %v", err)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

func lint(t *testing.T, setArgs ...string) error {
	t.Helper()
	helm := requireHelm(t)
	args := []string{"lint", chartDir, "--values", defaultValues}
	for _, s := range setArgs {
		args = append(args, "--set", s)
	}
	out, err := exec.CommandContext(t.Context(), helm, args...).CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}
	return nil
}

func find(t *testing.T, docs []manifest, kind, name string) manifest {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && str(d, "metadata", "name") == name {
			return d
		}
	}
	t.Fatalf("no %s %q in the render", kind, name)
	return nil
}

func asMap(v any) map[string]any {
	switch m := v.(type) {
	case manifest:
		return m
	case map[string]any:
		return m
	}
	return nil
}

func lookup(v any, path ...string) any {
	for _, key := range path {
		m := asMap(v)
		if m == nil {
			return nil
		}
		v = m[key]
	}
	return v
}

func str(v any, path ...string) string {
	s, _ := lookup(v, path...).(string)
	return s
}

func list(v any, path ...string) []any {
	l, _ := lookup(v, path...).([]any)
	return l
}

func stringMap(v any, path ...string) map[string]string {
	m := asMap(lookup(v, path...))
	out := make(map[string]string, len(m))
	for k, val := range m {
		s, _ := val.(string)
		out[k] = s
	}
	return out
}

func selects(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func gcSelectorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "opentams-gc",
		"app.kubernetes.io/instance": releaseName,
	}
}

func gcLabels() map[string]string {
	l := gcSelectorLabels()
	l["helm.sh/chart"] = chartLabel
	l["app.kubernetes.io/version"] = appVersion
	l["app.kubernetes.io/managed-by"] = "Helm"
	return l
}

func assertLabels(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s labels = %v, want %v", what, got, want)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s labels = %v, want %v", what, got, want)
			return
		}
	}
}

func gcContainer(t *testing.T, deployment manifest) any {
	t.Helper()
	containers := list(deployment, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("GC Deployment has %d containers, want 1", len(containers))
	}
	return containers[0]
}

func envValues(container any) map[string]string {
	out := map[string]string{}
	for _, e := range list(container, "env") {
		if v, ok := lookup(e, "value").(string); ok {
			out[str(e, "name")] = v
		}
	}
	return out
}

func isGCObject(d manifest) bool {
	return strings.HasSuffix(str(d, "metadata", "name"), "-gc")
}

// TC-GC-180: Default render
func TestGCDefaultRender(t *testing.T) {
	docs := render(t)
	deployment := find(t, docs, "Deployment", gcFullname)
	container := gcContainer(t, deployment)

	args := list(container, "args")
	if len(args) != 1 || args[0] != "gc" {
		t.Errorf("args = %v, want [gc]", args)
	}
	if got := str(container, "livenessProbe", "httpGet", "path"); got != "/healthz" {
		t.Errorf("liveness path = %q, want /healthz", got)
	}
	if got := str(container, "readinessProbe", "httpGet", "path"); got != "/readyz" {
		t.Errorf("readiness path = %q, want /readyz", got)
	}
	if lookup(container, "startupProbe") != nil {
		t.Error("GC container has a startup probe, want none")
	}

	env := envValues(container)
	for name, want := range map[string]string{
		"GC_POLL_INTERVAL":    "15m",
		"GC_BATCH_SIZE":       "1000",
		"GC_ALLOCATION_GRACE": "5m",
		"DB_HOST":             "postgres.default.svc",
	} {
		if env[name] != want {
			t.Errorf("env %s = %q, want %q", name, env[name], want)
		}
	}

	assertLabels(t, "GC Deployment metadata", stringMap(deployment, "metadata", "labels"), gcLabels())
	assertLabels(t, "GC Deployment selector", stringMap(deployment, "spec", "selector", "matchLabels"), gcSelectorLabels())
	podLabels := stringMap(deployment, "spec", "template", "metadata", "labels")
	assertLabels(t, "GC pod", podLabels, gcSelectorLabels())

	for _, d := range docs {
		if !isGCObject(d) {
			continue
		}
		if got := stringMap(d, "metadata", "labels")["app.kubernetes.io/name"]; got == "opentams" {
			t.Errorf("%s %s has label app.kubernetes.io/name: opentams", d["kind"], str(d, "metadata", "name"))
		}
	}

	apiService := find(t, docs, "Service", fullname)
	if selects(stringMap(apiService, "spec", "selector"), podLabels) {
		t.Errorf("API Service selector %v matches the GC pod labels %v", stringMap(apiService, "spec", "selector"), podLabels)
	}

	gcService := find(t, docs, "Service", gcFullname)
	assertLabels(t, "GC Service metadata", stringMap(gcService, "metadata", "labels"), gcLabels())
	if !selects(stringMap(gcService, "spec", "selector"), podLabels) {
		t.Errorf("GC Service selector %v does not match the GC pod labels %v", stringMap(gcService, "spec", "selector"), podLabels)
	}
	ports := list(gcService, "spec", "ports")
	if len(ports) != 1 || str(ports[0], "name") != "http" ||
		lookup(ports[0], "port") != 8080 || lookup(ports[0], "targetPort") != 8080 {
		t.Errorf("GC Service ports = %v, want one port http 8080 -> 8080", ports)
	}
}

// TC-GC-181: Disabled
func TestGCDisabled(t *testing.T) {
	docs := render(t,
		"gc.enabled=false",
		"serviceMonitor.enabled=true",
		"networkPolicy.enabled=true",
		"networkPolicy.monitoringNamespaceSelector.matchLabels.team=monitoring",
	)
	for _, d := range docs {
		if isGCObject(d) {
			t.Errorf("gc.enabled=false renders %s %s", d["kind"], str(d, "metadata", "name"))
		}
	}
}

// TC-GC-182: ServiceMonitor
func TestGCServiceMonitor(t *testing.T) {
	docs := render(t, "serviceMonitor.enabled=true", "serviceMonitor.labels.release=prometheus")
	monitor := find(t, docs, "ServiceMonitor", gcFullname)
	gcService := find(t, docs, "Service", gcFullname)

	wantLabels := gcLabels()
	wantLabels["release"] = "prometheus"
	assertLabels(t, "GC ServiceMonitor metadata", stringMap(monitor, "metadata", "labels"), wantLabels)

	if !selects(stringMap(monitor, "spec", "selector", "matchLabels"), stringMap(gcService, "metadata", "labels")) {
		t.Errorf("GC ServiceMonitor selector does not match the GC Service labels")
	}
	endpoints := list(monitor, "spec", "endpoints")
	if len(endpoints) != 1 {
		t.Fatalf("GC ServiceMonitor has %d endpoints, want 1", len(endpoints))
	}
	ep := endpoints[0]
	if str(ep, "port") != "http" || str(ep, "path") != "/metrics" ||
		str(ep, "interval") != "30s" || str(ep, "scrapeTimeout") != "10s" {
		t.Errorf("GC ServiceMonitor endpoint = %v, want port http, path /metrics, interval 30s, scrapeTimeout 10s", ep)
	}
}

// TC-GC-183: Schema
func TestGCValuesSchema(t *testing.T) {
	for _, set := range []string{"gc.batchSize=1001", "gc.batchSize=0", "gc.replicaCount=-1", "gc.enabled=maybe"} {
		t.Run(set+" rejected", func(t *testing.T) {
			if err := lint(t, set); err == nil {
				t.Errorf("helm lint --set %s gives no error", set)
			}
		})
	}
	for _, set := range []string{"gc.batchSize=1000", "gc.batchSize=1", "gc.replicaCount=0"} {
		t.Run(set+" accepted", func(t *testing.T) {
			if err := lint(t, set); err != nil {
				t.Errorf("helm lint --set %s: %v", set, err)
			}
		})
	}
}

// TC-GC-184: The API ServiceMonitor does not select GC objects
func TestGCServiceMonitorIsolation(t *testing.T) {
	docs := render(t, "serviceMonitor.enabled=true")
	apiMonitor := stringMap(find(t, docs, "ServiceMonitor", fullname), "spec", "selector", "matchLabels")
	gcMonitor := stringMap(find(t, docs, "ServiceMonitor", gcFullname), "spec", "selector", "matchLabels")
	apiService := stringMap(find(t, docs, "Service", fullname), "metadata", "labels")
	gcService := stringMap(find(t, docs, "Service", gcFullname), "metadata", "labels")

	if selects(apiMonitor, gcService) {
		t.Errorf("API ServiceMonitor selector %v matches the GC Service labels %v", apiMonitor, gcService)
	}
	if !selects(gcMonitor, gcService) {
		t.Errorf("GC ServiceMonitor selector %v does not match the GC Service labels %v", gcMonitor, gcService)
	}
	if selects(gcMonitor, apiService) {
		t.Errorf("GC ServiceMonitor selector %v matches the API Service labels %v", gcMonitor, apiService)
	}
}

// TC-GC-185: Scrape annotations by default
func TestGCServiceScrapeAnnotations(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		annotations := stringMap(find(t, render(t), "Service", gcFullname), "metadata", "annotations")
		for k, want := range map[string]string{
			"prometheus.io/scrape": "true",
			"prometheus.io/port":   "8080",
			"prometheus.io/path":   "/metrics",
		} {
			if annotations[k] != want {
				t.Errorf("annotation %s = %q, want %q", k, annotations[k], want)
			}
		}
	})
	t.Run("serviceMonitor enabled", func(t *testing.T) {
		annotations := stringMap(find(t, render(t, "serviceMonitor.enabled=true"), "Service", gcFullname), "metadata", "annotations")
		for k := range annotations {
			if strings.HasPrefix(k, "prometheus.io/") {
				t.Errorf("GC Service has annotation %s with serviceMonitor.enabled=true", k)
			}
		}
	})
}

// TC-GC-186: TLS does not reach the GC pod
func TestGCDeploymentWithoutTLS(t *testing.T) {
	docs := render(t, "server.tls.enabled=true", "server.tls.existingSecret=tls")

	apiVolumes := list(find(t, docs, "Deployment", fullname), "spec", "template", "spec", "volumes")
	if !slices.ContainsFunc(apiVolumes, func(v any) bool { return str(v, "secret", "secretName") == "tls" }) {
		t.Fatal("API Deployment has no TLS volume: the TLS values do not take effect")
	}

	deployment := find(t, docs, "Deployment", gcFullname)
	for _, v := range list(deployment, "spec", "template", "spec", "volumes") {
		if str(v, "name") == "tls" || str(v, "secret", "secretName") == "tls" {
			t.Errorf("GC Deployment has TLS volume %v", v)
		}
	}
	container := gcContainer(t, deployment)
	for _, m := range list(container, "volumeMounts") {
		if str(m, "name") == "tls" || strings.HasPrefix(str(m, "mountPath"), "/etc/opentams/tls") {
			t.Errorf("GC container has TLS volume mount %v", m)
		}
	}
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		if scheme := str(container, probe, "httpGet", "scheme"); scheme == "HTTPS" {
			t.Errorf("%s scheme = HTTPS, want HTTP", probe)
		}
	}
}

// Logic model section 12.3, gc-networkpolicy.yaml.
func TestGCNetworkPolicy(t *testing.T) {
	t.Run("monitoring namespace only", func(t *testing.T) {
		docs := render(t,
			"networkPolicy.enabled=true",
			"networkPolicy.ingressNamespaceSelector.matchLabels.team=ingress",
			"networkPolicy.monitoringNamespaceSelector.matchLabels.team=monitoring",
		)
		policy := find(t, docs, "NetworkPolicy", gcFullname)
		assertLabels(t, "GC NetworkPolicy metadata", stringMap(policy, "metadata", "labels"), gcLabels())
		assertLabels(t, "GC NetworkPolicy pod selector", stringMap(policy, "spec", "podSelector", "matchLabels"), gcSelectorLabels())

		ingress := list(policy, "spec", "ingress")
		if len(ingress) != 1 {
			t.Fatalf("GC NetworkPolicy has %d ingress rules, want 1", len(ingress))
		}
		from := list(ingress[0], "from")
		if len(from) != 1 || stringMap(from[0], "namespaceSelector", "matchLabels")["team"] != "monitoring" {
			t.Errorf("GC NetworkPolicy from = %v, want only the monitoring namespace", from)
		}
		ports := list(ingress[0], "ports")
		if len(ports) != 1 || lookup(ports[0], "port") != 8080 {
			t.Errorf("GC NetworkPolicy ports = %v, want 8080", ports)
		}
	})
	t.Run("no monitoring namespace denies all ingress", func(t *testing.T) {
		docs := render(t,
			"networkPolicy.enabled=true",
			"networkPolicy.ingressNamespaceSelector.matchLabels.team=ingress",
		)
		policy := find(t, docs, "NetworkPolicy", gcFullname)
		if ingress := list(policy, "spec", "ingress"); len(ingress) != 0 {
			t.Errorf("GC NetworkPolicy ingress = %v, want no rule", ingress)
		}
	})
}

const (
	notesWarning        = "NetworkPolicy blocks Prometheus from the API and GC pods. Set\nnetworkPolicy.monitoringNamespaceSelector to scrape metrics."
	notesWarningAPIOnly = "NetworkPolicy blocks Prometheus from the API pods. Set\nnetworkPolicy.monitoringNamespaceSelector to scrape metrics."
)

// renderNotes renders templates/NOTES.txt without a cluster. helm template
// skips NOTES.txt, so a copy of the chart wraps it in a named template and
// renders it as the value of a YAML document.
func renderNotes(t *testing.T, setArgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(chartDir)); err != nil {
		t.Fatalf("copy chart: %v", err)
	}
	notes, err := os.ReadFile(filepath.Join(dir, "templates", "NOTES.txt"))
	if err != nil {
		t.Fatalf("read NOTES.txt: %v", err)
	}
	wrapped := `{{ define "test.notes" }}` + string(notes) + `{{ end }}`
	if err := os.WriteFile(filepath.Join(dir, "templates", "_test_notes.tpl"), []byte(wrapped), 0o600); err != nil {
		t.Fatalf("write notes template: %v", err)
	}
	doc := "kind: TestNotes\nnotes: {{ include \"test.notes\" . | toJson }}\n"
	if err := os.WriteFile(filepath.Join(dir, "templates", "test-notes.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatalf("write notes document: %v", err)
	}

	helm := requireHelm(t)
	args := []string{"template", releaseName, dir, "--values", filepath.Join(dir, defaultValues), "--show-only", "templates/test-notes.yaml"}
	for _, a := range setArgs {
		args = append(args, "--set", a)
	}
	cmd := exec.CommandContext(t.Context(), helm, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var rendered struct {
		Notes string `yaml:"notes"`
	}
	if err := yaml.Unmarshal(out, &rendered); err != nil {
		t.Fatalf("decode rendered notes: %v", err)
	}
	if rendered.Notes == "" {
		t.Fatal("rendered notes are empty")
	}
	return rendered.Notes
}

func TestNotesNetworkPolicyMonitoringWarning(t *testing.T) {
	tests := []struct {
		name        string
		set         []string
		wantWarning bool
	}{
		{
			name:        "only the ingress namespace selector is set",
			set:         []string{"networkPolicy.enabled=true", "networkPolicy.ingressNamespaceSelector.matchLabels.team=ingress"},
			wantWarning: true,
		},
		{
			name:        "the monitoring namespace selector is set",
			set:         []string{"networkPolicy.enabled=true", "networkPolicy.monitoringNamespaceSelector.matchLabels.team=monitoring"},
			wantWarning: false,
		},
		{
			name:        "the NetworkPolicy is disabled",
			set:         []string{"networkPolicy.enabled=false"},
			wantWarning: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			notes := renderNotes(t, tc.set...)
			if got := strings.Contains(notes, notesWarning); got != tc.wantWarning {
				t.Errorf("NOTES contains the monitoring warning = %v, want %v\n%s", got, tc.wantWarning, notes)
			}
		})
	}
}

func TestNotesNetworkPolicyMonitoringWarningWithoutGC(t *testing.T) {
	notes := renderNotes(t,
		"gc.enabled=false",
		"networkPolicy.enabled=true",
		"networkPolicy.ingressNamespaceSelector.matchLabels.team=ingress",
	)
	if !strings.Contains(notes, notesWarningAPIOnly) {
		t.Errorf("NOTES has no API-only monitoring warning\n%s", notes)
	}
	if strings.Contains(notes, "API and GC pods") {
		t.Errorf("NOTES names the GC pods, but gc.enabled is false\n%s", notes)
	}
}

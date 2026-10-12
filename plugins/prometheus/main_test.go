package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mantonx/nexus-open/pkg/plugin"
)

// fakeProm serves canned /api/v1/query responses keyed by query string.
func fakeProm(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		body, ok := responses[r.URL.Query().Get("query")]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"status":"error","errorType":"bad_data","error":"unexpected query"}`)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func vector(pairs ...string) string {
	// pairs: instance, value, instance, value...
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, fmt.Sprintf(`{"metric":{"instance":%q},"value":[1760000000,%q]}`, pairs[i], pairs[i+1]))
	}
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(parts, ",") + `]}}`
}

func configured(t *testing.T, url string, cfg map[string]any) *PrometheusPlugin {
	t.Helper()
	p := newPrometheusPlugin()
	cfg["url"] = url
	if err := p.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return p
}

const cpuQuery = `100 - avg by (instance)(rate(node_cpu_seconds_total{mode="idle"}[1m]))*100`

func TestSample_AverageWithTopNode(t *testing.T) {
	srv := fakeProm(t, map[string]string{
		cpuQuery: vector("10.0.0.11:9100", "20", "10.0.0.12:9100", "70", "10.0.0.13:9100", "30", "10.0.0.14:9100", "40"),
	})
	p := configured(t, srv.URL, map[string]any{
		"query": cpuQuery, "reduce": "avg", "unit": "%", "label": "CPU",
		"top_label": "instance", "warn": 75, "crit": 90, "spark_max": 100,
	})

	got, err := p.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "40" || got.ValueUnit != "%" {
		t.Errorf("value = %q%q, want 40%%", got.Value, got.ValueUnit)
	}
	if got.Secondary != "CPU · 10.0.0.12 70%" {
		t.Errorf("secondary = %q", got.Secondary)
	}
	if got.Severity != plugin.SeverityOK {
		t.Errorf("severity = %q, want ok", got.Severity)
	}
	if len(got.Spark) != 1 || got.Spark[0] != 0.4 {
		t.Errorf("spark = %v, want [0.4]", got.Spark)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("payload invalid: %v", err)
	}
}

func TestSample_MaxTemperatureThresholds(t *testing.T) {
	q := `max by (instance)(node_thermal_zone_temp)`
	tests := []struct {
		hottest string
		want    plugin.Severity
	}{
		{"55.0", plugin.SeverityOK},
		{"72.5", plugin.SeverityWarn},
		{"86", plugin.SeverityCrit},
	}
	for _, tt := range tests {
		srv := fakeProm(t, map[string]string{q: vector("node1", "40", "node3", tt.hottest)})
		p := configured(t, srv.URL, map[string]any{
			"query": q, "reduce": "max", "unit": "°C", "decimals": 1, "warn": "70", "crit": "85",
		})
		got, err := p.Sample()
		if err != nil {
			t.Fatal(err)
		}
		if got.Severity != tt.want {
			t.Errorf("hottest %s: severity %q, want %q", tt.hottest, got.Severity, tt.want)
		}
	}
}

func TestSample_ReadyNodesWithProblemPods(t *testing.T) {
	ready := `sum(kube_node_status_condition{condition="Ready",status="true"})`
	total := `count(kube_node_info)`
	pods := `count(kube_pod_status_phase{phase=~"Pending|Failed|Unknown"} == 1)`
	scalar := func(v string) string {
		return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1760000000,"` + v + `"]}]}}`
	}
	empty := `{"status":"success","data":{"resultType":"vector","result":[]}}`
	cfg := func() map[string]any {
		return map[string]any{
			"query": ready, "reduce": "sum", "label": "nodes",
			"total_query": total, "secondary_query": pods, "secondary_label": "pods down",
		}
	}

	// All healthy: the problem query returns an empty vector.
	srv := fakeProm(t, map[string]string{ready: scalar("4"), total: scalar("4"), pods: empty})
	got, err := configured(t, srv.URL, cfg()).Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "4/4" || got.Secondary != "nodes" || got.Severity != plugin.SeverityOK {
		t.Errorf("healthy: got %q / %q / %q", got.Value, got.Secondary, got.Severity)
	}

	// Pods down: warning.
	srv = fakeProm(t, map[string]string{ready: scalar("4"), total: scalar("4"), pods: scalar("2")})
	got, err = configured(t, srv.URL, cfg()).Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got.Secondary != "nodes · 2 pods down" || got.Severity != plugin.SeverityWarn {
		t.Errorf("pods down: got %q / %q", got.Secondary, got.Severity)
	}

	// A node not Ready: critical, even with pods down.
	srv = fakeProm(t, map[string]string{ready: scalar("3"), total: scalar("4"), pods: scalar("2")})
	got, err = configured(t, srv.URL, cfg()).Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "3/4" || got.Severity != plugin.SeverityCrit {
		t.Errorf("node down: got %q / %q", got.Value, got.Severity)
	}
}

func TestSample_ScalarResult(t *testing.T) {
	srv := fakeProm(t, map[string]string{"scalar(1+1)": `{"status":"success","data":{"resultType":"scalar","result":[1760000000,"2"]}}`})
	got, err := configured(t, srv.URL, map[string]any{"query": "scalar(1+1)"}).Sample()
	if err != nil || got.Value != "2" {
		t.Errorf("got %q, %v", got.Value, err)
	}
}

func TestSample_Errors(t *testing.T) {
	srv := fakeProm(t, map[string]string{
		"empty":  `{"status":"success","data":{"resultType":"vector","result":[]}}`,
		"matrix": `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
	})
	for _, q := range []string{"empty", "matrix", "rejected"} {
		if _, err := configured(t, srv.URL, map[string]any{"query": q, "reduce": "max"}).Sample(); err == nil {
			t.Errorf("query %q: expected error", q)
		}
	}
	if _, err := newPrometheusPlugin().Sample(); err == nil {
		t.Error("no query: expected error")
	}
	if _, err := configured(t, "http://127.0.0.1:1", map[string]any{"query": "up", "timeout_ms": 200}).Sample(); err == nil {
		t.Error("unreachable: expected error")
	}
}

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(map[string]any{
		"query": " up ", "decimals": 2, "warn": "1.5", "crit": float64(3),
		"lower_is_worse": true, "_zone_width": 160,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.query != "up" || cfg.decimals != 2 || cfg.limits.warn != 1.5 || cfg.limits.crit != 3 ||
		!cfg.limits.lowerIsWorse || cfg.url != defaultURL || cfg.reduce != reduceAvg {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if _, err := parseConfig(map[string]any{"reduce": "median"}); err == nil {
		t.Error("bad reduce: expected error")
	}
	if _, err := parseConfig(map[string]any{"warn": "hot"}); err == nil {
		t.Error("bad number: expected error")
	}
}

func TestThresholdsLowerIsWorse(t *testing.T) {
	th := thresholds{warn: 3, crit: 2, lowerIsWorse: true}
	for v, want := range map[float64]string{4: "ok", 3: "warn", 2: "crit", 0: "crit"} {
		if got := th.classify(v); got != want {
			t.Errorf("classify(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestShortLabel(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.11:9100": "10.0.0.11",
		"node1":          "node1",
		"[fd00::1]:9100": "[fd00::1]",
		"[fd00::1]":      "[fd00::1]",
	} {
		if got := shortLabel(in); got != want {
			t.Errorf("shortLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

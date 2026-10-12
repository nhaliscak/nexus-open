// prometheus shows the result of a PromQL instant query in a zone. One zone
// is one query, so a page of zones becomes a cluster dashboard: average CPU
// across nodes, hottest SoC, Ready node count, and so on.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/mantonx/nexus-open/pkg/plugin"
)

const (
	defaultURL     = "http://kube-prometheus-stack-prometheus.monitoring:9090"
	defaultTimeout = 3 * time.Second
	// defaultTTL must outlast the slowest zone refresh (cluster.yaml goes up
	// to 60 s), or the host blanks the tile as "Stale" between samples.
	defaultTTL   = 2 * time.Minute
	sparkSamples = 60
)

// config is one zone's settings, parsed from plugin_config.
type config struct {
	url            string
	query          string
	reduce         reducer
	unit           string
	decimals       int
	label          string
	topLabel       string // series label naming the extreme series ("" = off)
	topLowest      bool
	limits         thresholds
	totalQuery     string // shown as "value/total"; value < total is critical
	secondaryQuery string // count shown in the secondary line
	secondaryLabel string
	details        []detail // extra values appended to the secondary line
	sparkMax       float64  // fixed graph ceiling; 0 = autoscale
	timeout        time.Duration
	ttl            time.Duration // how long a sample stays valid on screen
}

// urlEnv overrides defaultURL for every zone that doesn't set url itself.
// Cluster DNS names like defaultURL only resolve inside pods, so a daemon
// running on a k3s node's host sets this to the service's ClusterIP.
const urlEnv = "NEXUS_PROMETHEUS_URL"

func defaultConfig() config {
	u := defaultURL
	if env := os.Getenv(urlEnv); env != "" {
		u = env
	}
	return config{url: u, reduce: reduceAvg, timeout: defaultTimeout, ttl: defaultTTL}
}

// detail is an extra value shown on the secondary line, e.g. a node's
// temperature under its CPU tile.
type detail struct {
	query, label, unit string
	limits             thresholds // a hot detail can raise the tile's severity
}

// detailKeys are the config key prefixes for details, in display order.
// Plain string keys (not a list) because zone config crosses net/rpc as
// map[string]any, which can't carry nested lists without gob registration.
var detailKeys = []string{"detail", "detail2"}

// PrometheusPlugin implements plugin.Plugin.
type PrometheusPlugin struct {
	mu      sync.Mutex
	cfg     config
	client  *promClient
	history plugin.SparkHistory
}

func newPrometheusPlugin() *PrometheusPlugin {
	cfg := defaultConfig()
	return &PrometheusPlugin{
		cfg:     cfg,
		client:  newPromClient(cfg.url, cfg.timeout),
		history: plugin.NewSparkHistory(sparkSamples),
	}
}

func intPtr(v int) *int { return &v }

// Describe returns plugin metadata.
func (p *PrometheusPlugin) Describe() (plugin.Descriptor, error) {
	return plugin.Descriptor{
		Name:        "Prometheus",
		Version:     "1.0.0",
		Author:      "Nexus Open",
		Description: "Shows a PromQL query result, e.g. cluster CPU, temperature or Ready nodes",
		Icon:        "chart-line",
		RefreshMs:   5000,
		HasGraph:    true,
		Schema: plugin.ConfigSchema{Fields: []plugin.ConfigField{
			{Key: "url", Label: "Prometheus URL", Type: plugin.FieldTypeString, Default: defaultURL},
			{Key: "query", Label: "PromQL query", Type: plugin.FieldTypeString,
				Help: "Instant vector or scalar, e.g. avg(node_load1)"},
			{Key: "reduce", Label: "Combine series", Type: plugin.FieldTypeEnum, Default: string(reduceAvg),
				Options: []plugin.FieldOption{
					{Value: "avg", Label: "Average"}, {Value: "max", Label: "Maximum"},
					{Value: "min", Label: "Minimum"}, {Value: "sum", Label: "Sum"},
					{Value: "count", Label: "Count"},
				}},
			{Key: "label", Label: "Label", Type: plugin.FieldTypeString, Help: "Shown under the value"},
			{Key: "unit", Label: "Unit", Type: plugin.FieldTypeString, Help: "e.g. %, °C"},
			{Key: "decimals", Label: "Decimals", Type: plugin.FieldTypeInt, Default: 0, Min: intPtr(0), Max: intPtr(3)},
			{Key: "top_label", Label: "Name top series by", Type: plugin.FieldTypeString,
				Help: "Series label (e.g. instance) used to name the highest series"},
			{Key: "warn", Label: "Warn at", Type: plugin.FieldTypeString},
			{Key: "crit", Label: "Critical at", Type: plugin.FieldTypeString},
			{Key: "lower_is_worse", Label: "Lower is worse", Type: plugin.FieldTypeBool, Default: false},
			{Key: "total_query", Label: "Total query", Type: plugin.FieldTypeString,
				Help: "Optional; shows value/total and goes critical when value < total"},
			{Key: "secondary_query", Label: "Problem count query", Type: plugin.FieldTypeString,
				Help: "Optional; a non-zero result is shown and raises a warning"},
			{Key: "secondary_label", Label: "Problem count label", Type: plugin.FieldTypeString},
			{Key: "detail_query", Label: "Detail query", Type: plugin.FieldTypeString,
				Help: "Optional; a value shown on the secondary line (max of the series)"},
			{Key: "detail_label", Label: "Detail label", Type: plugin.FieldTypeString},
			{Key: "detail_unit", Label: "Detail unit", Type: plugin.FieldTypeString},
			{Key: "detail_warn", Label: "Detail warn at", Type: plugin.FieldTypeString},
			{Key: "detail_crit", Label: "Detail critical at", Type: plugin.FieldTypeString},
			{Key: "detail2_query", Label: "Second detail query", Type: plugin.FieldTypeString},
			{Key: "detail2_label", Label: "Second detail label", Type: plugin.FieldTypeString},
			{Key: "detail2_unit", Label: "Second detail unit", Type: plugin.FieldTypeString},
			{Key: "detail2_warn", Label: "Second detail warn at", Type: plugin.FieldTypeString},
			{Key: "detail2_crit", Label: "Second detail critical at", Type: plugin.FieldTypeString},
			{Key: "spark_max", Label: "Graph ceiling", Type: plugin.FieldTypeString,
				Help: "Fixed top of the graph (e.g. 100 for %); empty autoscales"},
		}},
	}, nil
}

// Configure applies plugin_config. Unknown keys (including host-injected
// zone dimensions) are ignored.
func (p *PrometheusPlugin) Configure(raw map[string]any) error {
	cfg, err := parseConfig(raw)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg.url != p.cfg.url || cfg.timeout != p.cfg.timeout {
		p.client = newPromClient(cfg.url, cfg.timeout)
	}
	if cfg.query != p.cfg.query {
		p.history = plugin.NewSparkHistory(sparkSamples)
	}
	p.cfg = cfg
	return nil
}

func parseConfig(raw map[string]any) (config, error) {
	cfg := defaultConfig()
	var err error
	str := func(key string, dst *string) {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	num := func(key string, dst *float64) {
		if err != nil {
			return
		}
		if v, ok := raw[key]; ok && v != nil && v != "" {
			*dst, err = toFloat(v)
			if err != nil {
				err = fmt.Errorf("%s: %w", key, err)
			}
		}
	}

	str("url", &cfg.url)
	str("query", &cfg.query)
	str("unit", &cfg.unit)
	str("label", &cfg.label)
	str("top_label", &cfg.topLabel)
	str("total_query", &cfg.totalQuery)
	str("secondary_query", &cfg.secondaryQuery)
	str("secondary_label", &cfg.secondaryLabel)
	for _, k := range detailKeys {
		var d detail
		str(k+"_query", &d.query)
		str(k+"_label", &d.label)
		str(k+"_unit", &d.unit)
		num(k+"_warn", &d.limits.warn)
		num(k+"_crit", &d.limits.crit)
		if d.query != "" {
			cfg.details = append(cfg.details, d)
		}
	}

	var reduce string
	str("reduce", &reduce)
	if reduce != "" {
		switch r := reducer(reduce); r {
		case reduceMax, reduceMin, reduceAvg, reduceSum, reduceCount:
			cfg.reduce = r
		default:
			return cfg, fmt.Errorf("reduce: unknown value %q", reduce)
		}
	}

	var decimals, timeoutMs float64
	num("decimals", &decimals)
	num("warn", &cfg.limits.warn)
	num("crit", &cfg.limits.crit)
	num("spark_max", &cfg.sparkMax)
	num("timeout_ms", &timeoutMs)
	var ttlMs float64
	num("ttl_ms", &ttlMs)
	if err != nil {
		return cfg, err
	}
	cfg.decimals = int(decimals)
	if ttlMs > 0 {
		cfg.ttl = time.Duration(ttlMs) * time.Millisecond
	}
	if timeoutMs > 0 {
		cfg.timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	cfg.limits.lowerIsWorse, _ = raw["lower_is_worse"].(bool)
	cfg.topLowest = cfg.limits.lowerIsWorse
	return cfg, nil
}

// toFloat accepts the number shapes YAML and JSON configs decode to.
func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(n), 64)
	default:
		return 0, fmt.Errorf("want a number, got %T", v)
	}
}

// Sample runs the configured queries and builds the zone payload.
func (p *PrometheusPlugin) Sample() (plugin.Payload, error) {
	p.mu.Lock()
	cfg, client := p.cfg, p.client
	p.mu.Unlock()

	if cfg.query == "" {
		return plugin.Payload{}, fmt.Errorf("no query configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	samples, err := client.query(ctx, cfg.query)
	if err != nil {
		return plugin.Payload{}, err
	}
	value, err := cfg.reduce.apply(samples)
	if err != nil {
		return plugin.Payload{}, err
	}

	severity := cfg.limits.classify(value)
	display := formatValue(value, cfg.decimals)
	unit := cfg.unit
	var secondary []string
	if cfg.label != "" {
		secondary = append(secondary, cfg.label)
	}

	if cfg.totalQuery != "" {
		totalSamples, err := client.query(ctx, cfg.totalQuery)
		if err != nil {
			return plugin.Payload{}, fmt.Errorf("total query: %w", err)
		}
		total, err := reduceSum.apply(totalSamples)
		if err != nil {
			return plugin.Payload{}, fmt.Errorf("total query: %w", err)
		}
		display = formatValue(value, cfg.decimals) + "/" + formatValue(total, cfg.decimals)
		unit = ""
		if value < total {
			severity = "crit"
		}
	}

	if cfg.topLabel != "" && len(samples) > 1 {
		// A zero top value (no restarts anywhere) names nothing useful.
		if top, ok := extreme(samples, cfg.topLowest); ok && top.Value != 0 {
			if name := top.Labels[cfg.topLabel]; name != "" {
				secondary = append(secondary, shortLabel(name)+" "+formatValue(top.Value, cfg.decimals)+cfg.unit)
			}
		}
	}

	if cfg.secondaryQuery != "" {
		probSamples, err := client.query(ctx, cfg.secondaryQuery)
		if err != nil {
			return plugin.Payload{}, fmt.Errorf("problem count query: %w", err)
		}
		count, _ := reduceSum.apply(probSamples) // empty result means no problems
		if count > 0 {
			secondary = append(secondary, formatValue(count, 0)+" "+cfg.secondaryLabel)
			if severity == "ok" {
				severity = "warn"
			}
		}
	}

	for _, d := range cfg.details {
		ds, err := client.query(ctx, d.query)
		if err != nil {
			return plugin.Payload{}, fmt.Errorf("%s query: %w", d.label, err)
		}
		v, err := reduceMax.apply(ds)
		if err != nil {
			continue // a missing detail shouldn't blank the whole tile
		}
		text := formatValue(v, 0) + d.unit
		if d.label != "" {
			text = d.label + " " + text
		}
		secondary = append(secondary, text)
		severity = worse(severity, d.limits.classify(v))
	}

	p.mu.Lock()
	p.history.Push(float32(value))
	spark := p.spark(cfg.sparkMax)
	p.mu.Unlock()

	return plugin.Payload{
		Value:     display,
		ValueUnit: unit,
		Secondary: strings.Join(secondary, " · "),
		Severity:  plugin.Severity(severity),
		Spark:     spark,
		GraphType: plugin.GraphTypeArea,
		TTL:       cfg.ttl,
		Icon:      "chart-line",
		Timestamp: time.Now(),
	}, nil
}

// spark returns the history scaled to [0, 1], against a fixed ceiling when
// one is configured (so 50% CPU always sits mid-graph) or autoscaled.
// Caller holds p.mu.
func (p *PrometheusPlugin) spark(ceiling float64) []float32 {
	if ceiling <= 0 {
		return p.history.Normalized(5, 95, 1)
	}
	raw := p.history.Raw()
	for i, v := range raw {
		s := v / float32(ceiling)
		raw[i] = min(max(s, 0), 1)
	}
	return raw
}

func main() {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: plugin.Handshake,
		Plugins: goplugin.PluginSet{
			"plugin": &plugin.ExecPlugin{Impl: newPrometheusPlugin()},
		},
	})
}

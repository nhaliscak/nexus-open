# Prometheus Plugin

Shows the result of a PromQL instant query in a zone. One zone is one query,
so a page of zones becomes a dashboard for a whole cluster rather than the
machine the daemon runs on. See `configs/layouts/cluster.yaml` for a
four-zone Kubernetes example (CPU, memory, temperature, Ready nodes).

## Configuration (`plugin_config`)

| Key | Default | Meaning |
|---|---|---|
| `url` | `http://kube-prometheus-stack-prometheus.monitoring:9090` | Prometheus base URL |
| `query` | required | Instant vector or scalar query |
| `reduce` | `avg` | Combine series: `avg`, `max`, `min`, `sum`, `count` |
| `label` | | Text under the value |
| `unit` | | Suffix after the value, e.g. `%`, `°C` |
| `decimals` | `0` | Digits after the decimal point |
| `top_label` | | Series label (e.g. `instance`) naming the highest series, shown under the value |
| `warn`, `crit` | unset | Thresholds for warning/critical colouring |
| `lower_is_worse` | `false` | Thresholds trip at or below the value instead of at or above |
| `total_query` | | Shows `value/total`; critical when value < total |
| `secondary_query`, `secondary_label` | | A count; when non-zero it is shown and raises a warning |
| `spark_max` | autoscale | Fixed graph ceiling, e.g. `100` for percentages |
| `timeout_ms` | `3000` | Per-query HTTP timeout |

## Building

```bash
cd plugins/prometheus
go build -o nexus-prometheus .
go test ./...
```

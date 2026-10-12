package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sample is one series from an instant-query result.
type sample struct {
	Labels map[string]string
	Value  float64
}

// promClient runs instant queries against the Prometheus HTTP API.
type promClient struct {
	baseURL string
	http    *http.Client
}

func newPromClient(baseURL string, timeout time.Duration) *promClient {
	return &promClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// queryResponse mirrors the /api/v1/query response envelope.
type queryResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// query runs an instant query and returns its samples. Vector and scalar
// results are supported; matrix and string results are rejected because a
// tile shows a single current value.
func (c *promClient) query(ctx context.Context, q string) ([]sample, error) {
	u := c.baseURL + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus unreachable: %w", err)
	}
	defer resp.Body.Close()

	var body queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus %s: bad response: %w", resp.Status, err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus %s: %s: %s", resp.Status, body.ErrorType, body.Error)
	}
	return parseResult(body.Data.ResultType, body.Data.Result)
}

func parseResult(resultType string, raw json.RawMessage) ([]sample, error) {
	switch resultType {
	case "vector":
		var series []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(raw, &series); err != nil {
			return nil, fmt.Errorf("decode vector: %w", err)
		}
		out := make([]sample, 0, len(series))
		for _, s := range series {
			v, err := parseValue(s.Value)
			if err != nil {
				return nil, err
			}
			out = append(out, sample{Labels: s.Metric, Value: v})
		}
		return out, nil
	case "scalar":
		var pair [2]any
		if err := json.Unmarshal(raw, &pair); err != nil {
			return nil, fmt.Errorf("decode scalar: %w", err)
		}
		v, err := parseValue(pair)
		if err != nil {
			return nil, err
		}
		return []sample{{Value: v}}, nil
	default:
		return nil, fmt.Errorf("unsupported result type %q (use an instant vector or scalar)", resultType)
	}
}

// parseValue decodes a [timestamp, "value"] pair. Prometheus encodes sample
// values as strings so it can represent NaN and ±Inf.
func parseValue(pair [2]any) (float64, error) {
	s, ok := pair[1].(string)
	if !ok {
		return 0, fmt.Errorf("sample value is %T, want string", pair[1])
	}
	return strconv.ParseFloat(s, 64)
}

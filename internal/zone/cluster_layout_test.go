package zone

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestClusterLayoutLoads guards the shipped cluster layout against schema
// drift: it must parse and every zone must use the prometheus plugin with a
// query.
func TestClusterLayoutLoads(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "configs", "layouts", "cluster.yaml")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Pages) == 0 {
		t.Fatal("expected at least one page")
	}
	zones := 0
	for _, page := range cfg.Pages {
		for _, z := range page.Zones {
			zones++
			if z.Plugin != "exec:nexus-prometheus" {
				t.Errorf("zone %s: plugin %q", z.ID, z.Plugin)
			}
			if q, _ := z.PluginConfig["query"].(string); q == "" {
				t.Errorf("zone %s: missing query", z.ID)
			}
		}
	}
	if zones != 16 {
		t.Errorf("got %d zones, want 16", zones)
	}
}

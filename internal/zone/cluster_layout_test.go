package zone

import (
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mantonx/nexus-open/internal/store"
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

// TestSeedKeepsPluginConfig checks that seeding the store from YAML keeps each
// zone's plugin_config, which the sampler reads only from the store.
func TestSeedKeepsPluginConfig(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	cfg, err := LoadConfig(filepath.Join(filepath.Dir(file), "..", "..", "configs", "layouts", "cluster.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "nexus.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	if err := seedDBFromConfig(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("seedDBFromConfig: %v", err)
	}
	for _, page := range cfg.Pages {
		for _, z := range page.Zones {
			got, err := db.GetZonePluginConfig(z.ID)
			if err != nil {
				t.Fatalf("zone %s: %v", z.ID, err)
			}
			if got["query"] != z.PluginConfig["query"] {
				t.Errorf("zone %s: stored query %v, want %v", z.ID, got["query"], z.PluginConfig["query"])
			}
		}
	}
}

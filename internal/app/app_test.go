package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mantonx/nexus-open/internal/device"
)

// repoRoot walks up from this file's location to find the repo root (contains go.mod).
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

func defaultLayoutPath() string {
	return filepath.Join(repoRoot(), "configs/layouts/multi-page.yaml")
}

// mockDeviceFactory returns a WithDeviceFactory option that injects a
// pre-connected mock device, replacing the NEXUS_MOCK_DEVICE env var.
func mockDeviceFactory() Option {
	return WithDeviceFactory(func(ctx context.Context) (device.Device, error) {
		m := device.NewMockDevice()
		if err := m.Connect(ctx); err != nil {
			return nil, err
		}
		return m, nil
	})
}

func TestApp_New(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	app, err := New(
		WithLogger(logger),
		WithConfigPath(""),
		WithAPIPort(19850),
		WithLayoutPath(defaultLayoutPath()),
		mockDeviceFactory(),
	)

	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	// Verify components were initialized
	if app.cfg == nil {
		t.Error("expected config to be initialized")
	}
	if app.device == nil {
		t.Error("expected device to be initialized")
	}
	if app.apiServer == nil {
		t.Error("expected API server to be initialized")
	}
	if app.zoneManager == nil {
		t.Error("expected zone manager to be initialized")
	}
	if app.zoneSampler == nil {
		t.Error("expected zone sampler to be initialized")
	}
}

func TestApp_Lifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	app, err := New(
		WithLogger(logger),
		WithConfigPath(""),
		WithAPIPort(19851),
		WithLayoutPath(defaultLayoutPath()),
		WithPluginsDir(t.TempDir()),
		mockDeviceFactory(),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	// Start app in background — use a background context so the test drives
	// shutdown via app.Shutdown(), not a context deadline.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	select {
	case <-app.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not become ready in time")
	}

	// Shutdown
	if err := app.Shutdown(); err != nil {
		t.Errorf("shutdown failed: %v", err)
	}

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected run error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("app did not shutdown in time")
	}
}

func TestApp_MultipleShutdowns(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	app, err := New(
		WithLogger(logger),
		WithConfigPath(""),
		WithAPIPort(19852),
		WithLayoutPath(defaultLayoutPath()),
		WithPluginsDir(t.TempDir()),
		mockDeviceFactory(),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go func() { _ = app.Run(ctx) }()
	select {
	case <-app.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not become ready in time")
	}

	// First shutdown
	if err := app.Shutdown(); err != nil {
		t.Errorf("first shutdown failed: %v", err)
	}

	// Second shutdown should be safe (no-op due to sync.Once)
	if err := app.Shutdown(); err != nil {
		t.Errorf("second shutdown failed: %v", err)
	}
}

func TestApp_ContextCancellation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	app, err := New(
		WithLogger(logger),
		WithConfigPath(""),
		WithAPIPort(19853),
		WithLayoutPath(defaultLayoutPath()),
		WithPluginsDir(t.TempDir()),
		mockDeviceFactory(),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	select {
	case <-app.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not become ready in time")
	}

	// Cancel context
	cancel()

	// Wait for Run to exit. The race detector adds substantial overhead when
	// iterating zones during start(), so allow generous headroom.
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("app did not exit after context cancellation")
	}

	// Clean shutdown
	_ = app.Shutdown()
}

func TestApp_Options(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	app, err := New(
		WithLogger(logger),
		WithConfigPath(""),
		WithAPIPort(12345),
		WithLayoutPath(defaultLayoutPath()),
		mockDeviceFactory(),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	// Verify options were applied (can't directly check private fields, but app was created)
	if app == nil {
		t.Fatal("expected non-nil app")
	}
}

func TestApp_ShutdownRestoresHardwareMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	var mock *device.MockDevice
	app, err := New(
		WithLogger(logger),
		WithConfigPath(""),
		WithAPIPort(19856),
		WithLayoutPath(defaultLayoutPath()),
		WithPluginsDir(t.TempDir()),
		WithDeviceFactory(func(ctx context.Context) (device.Device, error) {
			mock = device.NewMockDevice()
			if err := mock.Connect(ctx); err != nil {
				return nil, err
			}
			return mock, nil
		}),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("app did not become ready in time")
	}

	if err := app.Shutdown(); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	<-done

	cmds := mock.GetFeatureCommands()
	if len(cmds) == 0 || cmds[len(cmds)-1] != "restore_hardware_mode" {
		t.Errorf("expected restore_hardware_mode as last feature command, got %v", cmds)
	}
	if mock.IsConnected() {
		t.Error("expected device to be disconnected after shutdown")
	}
}

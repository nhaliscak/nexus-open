// Package app provides the main application orchestration and dependency injection.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mantonx/nexus-open/internal/api"
	"github.com/mantonx/nexus-open/internal/device"
	settings "github.com/mantonx/nexus-open/internal/settings"
	"github.com/mantonx/nexus-open/internal/store"
	"github.com/mantonx/nexus-open/internal/touch"
	"github.com/mantonx/nexus-open/internal/zone"
)

// DeviceFactory constructs and connects a device using the given context.
type DeviceFactory func(ctx context.Context) (device.Device, error)

// App is the main application container that holds all dependencies.
// It follows the dependency injection pattern to manage component lifecycle.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc
	logger *slog.Logger

	// Configuration
	configPath    string
	apiPort       int
	layoutPath    string
	pluginsDir    string
	deviceFactory DeviceFactory

	// Components
	store        *store.DB
	cfg          *settings.Manager
	device       device.Device
	apiServer    *api.Server
	zoneCfg      *zone.ConfigManager
	zoneManager  *zone.Manager
	zoneSampler  *zone.Sampler
	touchHandler *touch.Handler

	// Lifecycle
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
	readyCh      chan struct{}
	wg           sync.WaitGroup
}

// New creates a new application instance with the given options.
func New(opts ...Option) (*App, error) {
	ctx, cancel := context.WithCancel(context.Background())

	app := &App{
		ctx:        ctx,
		cancel:     cancel,
		logger:     slog.Default(),
		configPath: "",
		apiPort:    1985,
		shutdownCh: make(chan struct{}),
		readyCh:    make(chan struct{}),
	}

	// Apply options
	for _, opt := range opts {
		opt(app)
	}

	// Initialize components
	if err := app.initialize(); err != nil {
		cancel()
		return nil, fmt.Errorf("initialization failed: %w", err)
	}

	return app, nil
}

// Run starts all application components and blocks until shutdown.
func (a *App) Run(ctx context.Context) error {
	a.logger.Info("application starting")

	// Start components
	if err := a.start(); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}

	// Block until context is canceled or shutdown is called
	var runErr error
	select {
	case <-ctx.Done():
		a.logger.Info("context canceled")
		runErr = ctx.Err()
	case <-a.shutdownCh:
		a.logger.Info("shutdown requested")
	}
	a.Shutdown() //nolint:errcheck
	return runErr
}

// Ready returns a channel that is closed once all components (API server, zone
// sampler, render loop) have started. Callers can block on it instead of
// polling the health endpoint.
func (a *App) Ready() <-chan struct{} {
	return a.readyCh
}

// APIServer returns the underlying API server, used by callers that need to
// wire channels (e.g. the tray manager listening for window-closed signals).
func (a *App) APIServer() *api.Server {
	return a.apiServer
}

// Shutdown gracefully stops all application components.
func (a *App) Shutdown() error {
	var shutdownErr error

	a.shutdownOnce.Do(func() {
		a.logger.Info("initiating shutdown")
		close(a.shutdownCh)
		a.cancel()

		// Stop sampler (kills plugin subprocesses) and API server.
		// Both can block if a plugin hangs or the OS is slow, so enforce a
		// deadline. Hardware shutdown proceeds regardless after the deadline.
		stopDone := make(chan struct{})
		go func() {
			a.zoneSampler.Stop()
			close(stopDone)
		}()

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		select {
		case <-stopDone:
		case <-stopCtx.Done():
			a.logger.Warn("sampler stop timed out after 10s; forcing shutdown")
		}

		if a.apiServer != nil {
			apiCtx, apiCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer apiCancel()
			a.apiServer.Shutdown(apiCtx) //nolint:errcheck
		}

		// Wait for render loop and device watcher goroutines to exit before
		// closing the HID device — they may be mid-write and still hold a reference.
		wgDone := make(chan struct{})
		go func() { a.wg.Wait(); close(wgDone) }()
		wgCtx, wgCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer wgCancel()
		select {
		case <-wgDone:
		case <-wgCtx.Done():
			a.logger.Warn("render loop did not exit cleanly within 5s")
		}

		// Hand the display back to the firmware so it shows its own animation
		// instead of the last frame we sent. Fall back to a blank frame if the
		// command fails, so the panel at least doesn't show stale content.
		if a.device != nil && a.device.IsConnected() {
			if err := a.device.RestoreHardwareMode(); err != nil {
				a.logger.Warn("failed to restore hardware mode, blanking instead", "error", err)
				blank := make([]byte, 640*48*4)
				if err := a.device.SendFrame(context.Background(), blank); err != nil {
					a.logger.Debug("failed to send blank frame on shutdown", "error", err)
				}
			}
		}

		// Close HID device last, after all goroutines have stopped.
		if a.device != nil {
			if err := a.device.Disconnect(); err != nil {
				a.logger.Error("error disconnecting device", "error", err)
			}
		}

		if a.store != nil {
			if err := a.store.Close(); err != nil {
				a.logger.Error("error closing store", "error", err)
			}
		}

		a.logger.Info("shutdown complete")
	})

	return shutdownErr
}

// initialize sets up all application components in dependency order.
func (a *App) initialize() error {
	a.logger.Debug("initializing application components")
	if err := a.initStoreAndSettings(); err != nil {
		return err
	}
	if err := a.initDevice(); err != nil {
		return err
	}
	if err := a.initAPI(); err != nil {
		return err
	}
	if err := a.initLayoutAndSampler(); err != nil {
		return err
	}
	a.wireCallbacks()
	a.initTouch()
	return nil
}

// initStoreAndSettings opens the SQLite store and builds the settings manager.
func (a *App) initStoreAndSettings() error {
	var err error
	a.store, err = store.Open(a.configPath, a.logger)
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}

	a.cfg, err = settings.NewManager(a.store, a.logger)
	if err != nil {
		return fmt.Errorf("failed to create config manager: %w", err)
	}

	// On first run import any existing config.yaml so upgrades are seamless.
	if a.store.IsFirstRun() {
		legacyYAML := a.configPath
		if legacyYAML == "" {
			if dir, e := os.UserConfigDir(); e == nil {
				legacyYAML = dir + "/nexus-open/config.yaml"
			}
		}
		if err := a.cfg.ImportFromYAML(legacyYAML, a.logger); err != nil {
			a.logger.Warn("settings: yaml import failed (continuing with defaults)", "error", err)
		}
	}

	a.zoneCfg = zone.NewConfigManager(a.store, a.logger)
	a.logger.Info("configuration loaded", "first_run", a.store.IsFirstRun())
	return nil
}

// initDevice constructs and connects the hardware device.
// If WithDeviceFactory was provided it is used; otherwise the env var
// NEXUS_MOCK_DEVICE=1 selects the mock, and the real USB device is the default.
func (a *App) initDevice() error {
	if a.deviceFactory != nil {
		dev, err := a.deviceFactory(a.ctx)
		if err != nil {
			return fmt.Errorf("device factory: %w", err)
		}
		a.device = dev
		a.logger.Info("device initialized via factory")
		return nil
	}

	if os.Getenv("NEXUS_MOCK_DEVICE") == "1" {
		a.logger.Info("using mock device (NEXUS_MOCK_DEVICE=1)")
		mock := device.NewMockDevice()
		if err := mock.Connect(a.ctx); err != nil {
			return fmt.Errorf("failed to connect mock device: %w", err)
		}
		a.device = mock
		return nil
	}

	a.device = device.NewNexusDevice(a.logger, device.ConnectionConfig{
		VendorID:         0x1b1c,
		ProductID:        0x1b8e,
		ReconnectRetries: 10,
		ReconnectDelay:   5 * time.Second,
	})
	a.logger.Info("device created")
	return nil
}

// initAPI creates the API server and registers the zone config manager.
func (a *App) initAPI() error {
	apiAddr := fmt.Sprintf("127.0.0.1:%d", a.apiPort)
	a.apiServer = api.NewServer(apiAddr, a.cfg, a.device, a.logger)
	a.apiServer.SetZoneConfigManager(a.zoneCfg)
	a.logger.Info("API server created", "addr", apiAddr)
	return nil
}

// initLayoutAndSampler creates the zone manager, resolves the plugins directory,
// and creates the zone sampler.
func (a *App) initLayoutAndSampler() error {
	if a.layoutPath == "" {
		a.layoutPath = resolveLayoutPath()
	}
	var err error
	a.zoneManager, err = zone.NewManager(a.ctx, a.logger, a.store, a.layoutPath)
	if err != nil {
		return fmt.Errorf("failed to create zone manager: %w", err)
	}
	a.logger.Info("zone manager initialized",
		"pages", len(a.zoneManager.GetConfig().Pages),
		"current_page", a.zoneManager.GetConfig().Pages[0].Name)

	// Priority: explicit option > NEXUS_PLUGINS_DIR env > sibling to exe >
	// XDG user data > /usr/lib/nexus-open/plugins (system package install).
	if a.pluginsDir == "" {
		if env := os.Getenv("NEXUS_PLUGINS_DIR"); env != "" {
			a.pluginsDir = env
		} else if exePath, err := os.Executable(); err == nil {
			sibling := filepath.Join(filepath.Dir(exePath), "plugins")
			xdgData := filepath.Join(os.Getenv("XDG_DATA_HOME"), "nexus-open", "plugins")
			if os.Getenv("XDG_DATA_HOME") == "" {
				xdgData = filepath.Join(os.Getenv("HOME"), ".local", "share", "nexus-open", "plugins")
			}
			systemData := "/usr/lib/nexus-open/plugins"
			switch {
			case dirHasPlugins(sibling):
				a.pluginsDir = sibling
			case dirHasPlugins(xdgData):
				a.pluginsDir = xdgData
			case dirHasPlugins(systemData):
				a.pluginsDir = systemData
			default:
				a.pluginsDir = sibling
			}
		}
	}
	a.logger.Info("plugins directory", "path", a.pluginsDir)

	a.zoneSampler = zone.NewSampler(a.ctx, a.logger, a.zoneManager, a.zoneCfg, a.pluginsDir)
	a.logger.Info("zone sampler created")
	return nil
}

// wireCallbacks connects the zone manager, sampler, and API server together.
func (a *App) wireCallbacks() {
	// Page change → broadcast new page state over WebSocket.
	a.zoneManager.SetOnPageChange(func(pageIndex int) error {
		go a.apiServer.BroadcastPageState()
		return nil
	})

	// Zone tap cycle → restart that zone's sampler.
	a.zoneManager.SetOnZoneCycle(func(zoneConfig zone.ZoneConfig) error {
		return a.zoneSampler.RestartZone(zoneConfig)
	})

	// Wire sampler into API server as config notifier, status provider, and catalog.
	a.apiServer.SetZoneConfigNotifier(a.zoneSampler)
	a.apiServer.SetZoneStatusProvider(a.zoneSampler)
	a.apiServer.SetPluginCatalog(a.zoneSampler)

	// Wire zone manager into API server for swipe/tap/navigation/layout.
	a.apiServer.SetSwipeSimulator(a.zoneManager)
	a.apiServer.SetZoneTapper(a.zoneManager)
	a.apiServer.SetDetailFrameProvider(a.zoneManager)
	a.apiServer.SetFrameProvider(a.zoneManager)
	a.apiServer.SetNavigator(a.zoneManager)
	a.apiServer.SetLayoutStore(a.store)
	a.apiServer.SetLayoutReloader(a.zoneManager)

	a.zoneManager.SetDetailStateCallback(func(active bool) {
		a.apiServer.BroadcastDetailState(active, zone.DetailCloseX*2, zone.DetailCloseY*2)
	})

	// Apply background image from settings immediately, then watch for future saves.
	applySettingsTheme := func(cfg settings.Config) {
		if cfg.BackgroundImage != "" && cfg.BackgroundImage != settings.DefaultBackgroundImage {
			if dir, err := os.UserConfigDir(); err == nil {
				imgPath := dir + "/nexus-open/images/" + cfg.BackgroundImage
				if err := a.zoneManager.SetBackground(imgPath); err != nil {
					a.logger.Warn("failed to set background image", "path", imgPath, "error", err)
				}
			}
		} else {
			a.zoneManager.SetBackground("") //nolint:errcheck
		}
	}
	applySettingsTheme(a.cfg.Get())
	settingsCh := make(chan settings.Config, 4)
	a.cfg.Watch(settingsCh)
	go func() {
		for cfg := range settingsCh {
			applySettingsTheme(cfg)
			a.logger.Debug("theme updated from settings", "bg", cfg.BackgroundColor, "fg", cfg.TextColor)
		}
	}()
}

// initTouch creates the touch handler.
func (a *App) initTouch() {
	a.zoneManager.SetPluginLookup(a.zoneSampler)
	a.touchHandler = touch.NewHandler(a.logger, a.device, a.zoneManager)
	a.logger.Info("touch handler created")
}

// start begins operation of all components.
func (a *App) start() error {
	a.logger.Debug("starting application components")

	// Start the API server first so Flutter and health checks are available
	// immediately — device connect can take several seconds on a slow replug.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := a.apiServer.Start(); err != nil {
			a.logger.Error("API server error", "error", err)
		}
	}()
	a.logger.Info("API server started")

	// Start module sampler
	if err := a.zoneSampler.Start(); err != nil {
		return fmt.Errorf("failed to start zone sampler: %w", err)
	}
	a.logger.Info("zone sampler started")

	// Start touch handler
	if err := a.touchHandler.Start(a.ctx); err != nil {
		return fmt.Errorf("failed to start touch handler: %w", err)
	}
	a.logger.Info("touch handler started")

	// Start zone rendering loop
	a.wg.Add(1)
	go a.renderLoop()
	a.logger.Info("zone rendering started")

	// Attempt device connect after everything else is running. If it fails,
	// the watcher retries every 3s so the device can be plugged in any time.
	if err := a.device.Connect(a.ctx); err != nil {
		a.logger.Warn("failed to connect to device", "error", err)
		a.apiServer.SetLastConnectError(err)
		a.startDeviceWatcher()
	}

	close(a.readyCh)
	return nil
}

// startDeviceWatcher polls for the device in the background until it connects.
// Called when the initial connect attempt fails (device not plugged in yet).
// Once connected, the device's own monitorConnection goroutine takes over.
func (a *App) startDeviceWatcher() {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

		const pollInterval = 3 * time.Second
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		a.logger.Info("device watcher started — waiting for device to be plugged in")

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				if a.device.IsConnected() {
					// Already connected (monitorConnection handled a replug).
					return
				}
				if err := a.device.Connect(a.ctx); err != nil {
					a.logger.Debug("device not available, will retry", "error", err)
					a.apiServer.SetLastConnectError(err)
				} else {
					a.logger.Info("device connected")
					a.apiServer.SetLastConnectError(nil)
					return
				}
			}
		}
	}()
}

// dirHasPlugins returns true only when path is a directory that contains at
// least one regular executable file. An empty or stale directory does not
// qualify — this prevents a leftover ~/.local/share/nexus-open/plugins/ from
// shadowing the system install at /usr/lib/nexus-open/plugins/.
func dirHasPlugins(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode()&0111 != 0 {
			return true
		}
	}
	return false
}

// resolveLayoutPath finds the fallback zone layout YAML using the same
// priority order as plugin resolution: XDG user install > system package
// install (/usr/share) > dev repo path relative to the executable.
func resolveLayoutPath() string {
	const layoutFile = "configs/layouts/multi-page.yaml"

	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	candidates := []string{
		filepath.Join(xdgData, "nexus-open", layoutFile),
		filepath.Join("/usr/share/nexus-open", layoutFile),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "..", layoutFile), // dev: repo root
		)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return layoutFile // last resort: relative path (dev CWD)
}

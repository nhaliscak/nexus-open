# Nexus Open - Makefile
# Standardized build system for all targets

.PHONY: help setup doctor build build-debug build-release build-ui build-plugins build-arm64 build-all test test-race coverage clean clean-ui install uninstall run run-tray dev dev-backend dev-ui dev-ui-reload deb rpm generate-api models all changelog release

# Configuration
APP_NAME := nexus-open
VERSION := $(shell git describe --tags --match 'v*' --always 2>/dev/null | sed 's/^v//;s/-[0-9]*-g[0-9a-f]*//' || echo "0.0.0-dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')

# Directories
BIN_DIR := bin
BUILD_DIR := build
DIST_DIR := dist
CMD_DIR := cmd/nexus-open
UI_DIR := ui
UI_BUILD_DIR := $(UI_DIR)/build/linux/x64/release/bundle

# Tool versions — keep in sync with .github/workflows/ci.yml
SQLC_VERSION         := v1.31.1
GOVULNCHECK_VERSION  := v1.3.0
GOLANGCI_VERSION     := v2.12.2
AIR_VERSION          := v1.63.1
GOPENAPI_VERSION     := v0.32.3

# Build flags
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)
GOFLAGS  := -trimpath
GO_BUILD := CGO_ENABLED=0 go build

# Default target
help:
	@echo "Nexus Open - Build System"
	@echo ""
	@echo "UI Testing:"
	@echo "  make screenshot-tour - Navigate all tabs and capture screenshots"
	@echo ""
	@echo "Development:"
	@echo "  make setup         - Install dev tool dependencies (air, overmind)"
	@echo "  make doctor        - Check runtime health (user) and toolchain (dev)"
	@echo "  make build         - Build Go backend only (with debug info)"
	@echo "  make build-ui      - Build Flutter UI only"
	@echo "  make build-plugins - Build all external plugins"
	@echo "  make build-all     - Build backend, UI, and all plugins"
	@echo "  make build-arm64   - Cross-compile headless daemon + plugins for linux/arm64"
	@echo "  make build-release - Build optimized release binary (stripped)"
	@echo "  make run           - Build and run Go backend only"
	@echo "  make run-tray      - Build and run bundled app with system tray"
	@echo "  make dev           - Start full dev environment (backend + UI + auto-reload)"
	@echo "  make dev-backend   - Go hot-reload only (air)"
	@echo "  make dev-ui        - Flutter runner only (writes PID for auto-reload)"
	@echo "  make dev-ui-reload - Dart file watcher only (watchexec → SIGUSR1)"
	@echo ""
	@echo "Testing:"
	@echo "  make test          - Run all tests"
	@echo "  make test-race     - Run tests with race detector"
	@echo "  make coverage      - Generate test coverage report"
	@echo ""
	@echo "Packaging:"
	@echo "  make deb           - Build DEB package"
	@echo "  make rpm           - Build RPM package"
	@echo "  make generate-api  - Regenerate api/openapi.yaml from annotations"
	@echo "  make generate-store - Regenerate internal/store/db/ from schema + queries"
	@echo "  make models        - Generate freezed/json_serializable Dart models"
	@echo "  make all           - Build all packages"
	@echo ""
	@echo "Maintenance:"
	@echo "  make clean         - Remove build artifacts"
	@echo "  make install       - Build and install system-wide (requires sudo), restart service"
	@echo "  make uninstall     - Stop service and remove system-wide install (requires sudo)"
	@echo ""
	@echo "Version: $(VERSION) (commit: $(COMMIT))"

# Create directories
$(BIN_DIR):
	@mkdir -p $(BIN_DIR)

$(DIST_DIR):
	@mkdir -p $(DIST_DIR)

# Build development binary (with debug info)
build: $(BIN_DIR)
	@echo "Building $(APP_NAME) (development)..."
	@$(GO_BUILD) $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP_NAME) ./$(CMD_DIR)
	@echo "✓ Built: $(BIN_DIR)/$(APP_NAME)"

# Build with debug symbols (alias)
build-debug: build

# Build optimized release binary
build-release: $(BIN_DIR)
	@echo "Building $(APP_NAME) (release)..."
	@$(GO_BUILD) $(GOFLAGS) -ldflags "$(LDFLAGS) -s -w" -o $(BIN_DIR)/$(APP_NAME) ./$(CMD_DIR)
	@strip $(BIN_DIR)/$(APP_NAME) 2>/dev/null || true
	@echo "✓ Built and stripped: $(BIN_DIR)/$(APP_NAME)"
	@ls -lh $(BIN_DIR)/$(APP_NAME)

# Build Flutter UI
build-ui: $(BIN_DIR)
	@echo "Building Flutter UI..."
	@if ! command -v flutter > /dev/null; then \
		echo "Error: Flutter not found. Install from https://flutter.dev"; \
		exit 1; \
	fi
	@cd $(UI_DIR) && flutter build linux --release --dart-define=APP_VERSION=$(VERSION)
	@echo "Copying Flutter bundle to bin directory..."
	@rm -rf $(BIN_DIR)/nexus-open-ui-bundle
	@cp -r $(UI_BUILD_DIR) $(BIN_DIR)/nexus-open-ui-bundle
	@ln -sf nexus-open-ui-bundle/ui $(BIN_DIR)/nexus-open-ui
	@echo "✓ Flutter UI built: $(BIN_DIR)/nexus-open-ui"

# Build all external plugins
build-plugins:
	@echo "Building external plugins..."
	@mkdir -p $(BIN_DIR)/plugins
	@for mod in cpu-temp gpu-temp network weather cpu-load gpu-load media prometheus; do \
		if [ -d plugins/$$mod ]; then \
			echo "  → plugins/$$mod"; \
			_ldf="$(LDFLAGS)"; \
			if [ "$$mod" = "media" ] && [ -n "$(TMDB_TOKEN)" ]; then \
				_ldf="$$_ldf -X main.bundledTMDbTokenHex=$$(scripts/xor-token.sh '$(TMDB_TOKEN)')"; \
			fi; \
			(cd plugins/$$mod && go build -ldflags "$$_ldf" -o ../../$(BIN_DIR)/plugins/nexus-$$mod .) || exit 1; \
		fi; \
	done
	@echo "✓ Plugins built"

# Cross-compile a headless daemon + plugins for 64-bit ARM SBCs (e.g. Turing RK1).
# No UI or tray; deploy with packaging/systemd/nexus-open-headless.service.
ARM64_DIR := $(DIST_DIR)/linux-arm64
build-arm64:
	@echo "Building $(APP_NAME) for linux/arm64..."
	@mkdir -p $(ARM64_DIR)/plugins
	@GOOS=linux GOARCH=arm64 $(GO_BUILD) $(GOFLAGS) -ldflags "$(LDFLAGS) -s -w" -o $(ARM64_DIR)/$(APP_NAME) ./$(CMD_DIR)
	@for mod in cpu-temp gpu-temp network weather cpu-load gpu-load media prometheus; do \
		if [ -d plugins/$$mod ]; then \
			echo "  → plugins/$$mod"; \
			(cd plugins/$$mod && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS) -s -w" -o ../../$(ARM64_DIR)/plugins/nexus-$$mod .) || exit 1; \
		fi; \
	done
	@cp packaging/systemd/nexus-open-headless.service packaging/udev/99-corsair-nexus-headless.rules $(ARM64_DIR)/
	@cp -r configs $(ARM64_DIR)/
	@echo "✓ Built: $(ARM64_DIR)/"

# Build backend, UI, and all plugins
build-all: build build-ui build-plugins
	@echo "✓ Complete build finished!"
	@ls -lh $(BIN_DIR)/

# Run the application (backend only)
run: build
	@echo "Running $(APP_NAME)..."
	@$(BIN_DIR)/$(APP_NAME)

# Run bundled application with system tray
run-tray: build-all
	@echo "Running $(APP_NAME) with system tray and UI..."
	@$(BIN_DIR)/$(APP_NAME) --tray

# Navigate all UI tabs and capture screenshots via Dart VM service.
# Requires: Go backend running (make run) and DISPLAY set.
screenshot-tour:
	@./scripts/ui-tour.sh

# Install dev tool dependencies
setup:
	@echo "Installing air (Go live reload)..."
	@go install github.com/air-verse/air@$(AIR_VERSION)
	@echo "Checking for overmind (process manager)..."
	@if ! command -v overmind > /dev/null; then \
		echo "overmind not found. Install it with:"; \
		echo "  Arch:         sudo pacman -S overmind"; \
		echo "  Debian/Ubuntu: sudo apt install overmind"; \
		echo "  macOS:        brew install overmind"; \
		echo "  Go:           go install github.com/DarthSim/overmind/v2@latest"; \
	else \
		echo "overmind already installed: $$(overmind --version)"; \
	fi
	@echo "Checking for watchexec (Flutter auto-reload watcher)..."

	@if ! command -v watchexec > /dev/null; then \
		echo "watchexec not found. Install it with:"; \
		echo "  Arch:         sudo pacman -S watchexec"; \
		echo "  Cargo:        cargo install watchexec-cli"; \
		echo "  Homebrew:     brew install watchexec"; \
	else \
		echo "watchexec already installed: $$(watchexec --version)"; \
	fi
	@echo "Installing git hooks..."
	@git config core.hooksPath scripts/hooks
	@chmod +x scripts/hooks/*
	@echo "✓ Setup complete. Run 'make dev-backend', 'make dev-ui', and 'make dev-ui-reload' to start."

# Check runtime health (end-user) and dev toolchain (contributor).
# Exits 1 if any required check fails, so it can be used in scripts.
doctor:
	@fail=0; \
	ok()   { printf '  \033[32m✓\033[0m %s\n' "$$*"; }; \
	warn() { printf '  \033[33m!\033[0m %s\n' "$$*"; }; \
	bad()  { printf '  \033[31m✗\033[0m %s\n' "$$*"; fail=1; }; \
	\
	echo "── Runtime ─────────────────────────────────────────"; \
	\
	if systemctl --user is-active --quiet nexus-open.service 2>/dev/null; then \
		ok "daemon running (systemd)"; \
	elif pgrep -x nexus-open > /dev/null 2>&1; then \
		ok "daemon running (standalone)"; \
	else \
		bad "daemon not running — run 'make install' to install system-wide, or 'make dev-backend' for dev"; \
	fi; \
	\
	if curl -sf -H "X-Nexus-Token: $$(cat ~/.config/nexus-open/token 2>/dev/null)" \
		http://localhost:1985/api/health > /dev/null 2>&1; then \
		ok "API reachable at localhost:1985"; \
	else \
		bad "API not reachable — is the daemon running?"; \
	fi; \
	\
	if [ -f ~/.config/nexus-open/token ]; then \
		ok "capability token present"; \
	else \
		bad "token missing at ~/.config/nexus-open/token — start the daemon once to generate it"; \
	fi; \
	\
	if lsusb 2>/dev/null | grep -q "1b1c:1b8e"; then \
		ok "Nexus device detected (USB 1b1c:1b8e)"; \
	else \
		warn "Nexus device not detected — not connected, or using mock mode"; \
	fi; \
	\
	if ls /etc/udev/rules.d/99-corsair-nexus.rules /usr/lib/udev/rules.d/99-corsair-nexus.rules > /dev/null 2>&1; then \
		ok "udev rules installed"; \
	else \
		bad "udev rules missing — run: sudo nexus-open --setup-udev"; \
	fi; \
	\
	if groups | grep -qE '\bplugdev\b'; then \
		ok "user in plugdev group"; \
	else \
		warn "user not in plugdev group (may still work via udev TAG+=\"uaccess\")"; \
	fi; \
	\
	echo ""; \
	echo "── Dev toolchain ───────────────────────────────────"; \
	\
	if command -v go > /dev/null; then \
		ok "go $$(go version | awk '{print $$3}')"; \
	else \
		bad "go not found"; \
	fi; \
	\
	if command -v flutter > /dev/null; then \
		ok "flutter $$(flutter --version --machine 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("frameworkVersion","?"))' 2>/dev/null || flutter --version 2>&1 | head -1)"; \
	else \
		bad "flutter not found — see https://flutter.dev/docs/get-started/install/linux"; \
	fi; \
	\
	AIR=$$(command -v air 2>/dev/null || echo ~/go/bin/air); \
	if [ -x "$$AIR" ]; then \
		ok "air $$($$AIR -v 2>&1 | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)"; \
	else \
		bad "air not found — run 'make setup'"; \
	fi; \
	\
	if command -v overmind > /dev/null; then \
		ok "overmind $$(overmind --version 2>&1)"; \
	else \
		bad "overmind not found — run 'make setup' for install instructions"; \
	fi; \
	\
	SQLC=$$(command -v sqlc 2>/dev/null || echo ~/go/bin/sqlc); \
	if [ -x "$$SQLC" ]; then \
		SQLC_INST=$$($$SQLC version 2>&1 | head -1); \
		ok "sqlc $$SQLC_INST"; \
	else \
		warn "sqlc not found (only needed to regenerate DB queries) — go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)"; \
	fi; \
	\
	if command -v watchexec > /dev/null; then \
		ok "watchexec $$(watchexec --version 2>&1)"; \
	else \
		warn "watchexec not found (needed for Flutter auto-reload) — sudo pacman -S watchexec"; \
	fi; \
	\
	if command -v golangci-lint > /dev/null; then \
		ok "golangci-lint $$(golangci-lint --version 2>&1 | head -1)"; \
	else \
		warn "golangci-lint not found (only needed for lint) — see https://golangci-lint.run/usage/install/"; \
	fi; \
	\
	echo ""; \
	if [ $$fail -eq 0 ]; then \
		echo "All checks passed."; \
	else \
		echo "Some checks failed — see above."; \
		exit 1; \
	fi

# Development mode with live reload (requires github.com/air-verse/air)
# Rebuilds and restarts the Go daemon + plugins on any .go file change.
# Plugins are loaded from /usr/lib/nexus-open/plugins at runtime;
# run 'make install' once first so the system layout is in place.
dev-backend:
	@AIR=$$(command -v air 2>/dev/null || echo ~/go/bin/air); \
	if [ ! -x "$$AIR" ]; then \
		echo "Error: 'air' not found. Install with: go install github.com/air-verse/air@$(AIR_VERSION)"; \
		exit 1; \
	fi; \
	systemctl --user stop nexus-open.service 2>/dev/null || true; \
	NEXUS_MOCK_DEVICE=0 NEXUS_DEBUG=1 "$$AIR"

# Flutter hot-reload UI (runs flutter run in debug mode).
# The backend must already be running (make run or make dev-backend).
# Token is read from ~/.config/nexus-open/token automatically.
# Writes its PID to /tmp/nexus-flutter.pid so dev-ui-reload can signal it.
dev-ui:
	@if [ ! -f ~/.config/nexus-open/token ]; then \
		echo "Error: token not found at ~/.config/nexus-open/token — is the backend running?"; \
		exit 1; \
	fi
	@cd ui && GDK_BACKEND=x11 flutter run -d linux --pid-file /tmp/nexus-flutter.pid \
		--dart-define=APP_VERSION=$(VERSION)

# Watch ui/lib for Dart changes and signal flutter run to hot-reload.
# SIGUSR1 = hot reload (preserves state), SIGUSR2 = hot restart (full reset).
# Run alongside dev-ui; watchexec debounces rapid multi-file saves automatically.
dev-ui-reload:
	@if ! command -v watchexec > /dev/null; then \
		echo "Error: 'watchexec' not found."; \
		echo "  Arch:   sudo pacman -S watchexec"; \
		echo "  Cargo:  cargo install watchexec-cli"; \
		exit 1; \
	fi
	@echo "Watching ui/lib for Dart changes (SIGUSR1 → hot reload, SIGUSR2 → hot restart)…"
	@watchexec --watch ui/lib --exts dart --no-process-group \
		'pid=$$(cat /tmp/nexus-flutter.pid 2>/dev/null); [ -n "$$pid" ] && kill -USR1 $$pid'

# Start the full dev environment: Go hot-reload (air), Flutter UI, and Dart
# file watcher (watchexec → SIGUSR1). All three run under overmind so logs are
# multiplexed and any process can be attached to with 'overmind connect <name>'.
dev:
	@overmind status 2>/dev/null && overmind echo || { rm -f .overmind.sock && overmind start; }

# Run all tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Run tests with race detector
test-race:
	@echo "Running tests with race detector..."
	@go test -race -v ./...

# Generate coverage report
coverage:
	@echo "Generating coverage report..."
	@go test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✓ Coverage report: coverage.html"
	@go tool cover -func=coverage.out | grep total:

# Build DEB package
deb: $(DIST_DIR)
	@echo "Building DEB package..."
	@bash scripts/build-deb.sh

# Build RPM package
rpm: $(DIST_DIR)
	@echo "Building RPM package..."
	@bash scripts/build-rpm.sh

# Regenerate OpenAPI spec from source annotations
generate-api:
	@echo "Regenerating OpenAPI spec..."
	@if command -v go-openapi > /dev/null || [ -f ~/go/bin/go-openapi ]; then \
		GO_OPENAPI=$$(command -v go-openapi 2>/dev/null || echo ~/go/bin/go-openapi); \
		$$GO_OPENAPI -dir cmd/nexus-open,internal/api,internal/settings -output api/openapi.yaml; \
		sed -i 's|http://localhost:8080|http://localhost:1985|g' api/openapi.yaml; \
		echo "✓ Spec written to api/openapi.yaml"; \
	else \
		echo "Error: go-openapi not found. Install with: go install github.com/go-openapi/cmd/go-openapi@$(GOPENAPI_VERSION)"; \
		exit 1; \
	fi

# Generate freezed/json_serializable Dart models from api_models.dart
models:
	@echo "Generating Dart models..."
	@cd $(UI_DIR) && dart run build_runner build --delete-conflicting-outputs
	@echo "✓ Models generated"

# Build all packages
all: deb rpm
	@echo "✓ All packages built successfully!"
	@ls -lh $(DIST_DIR)/

# Install system-wide (requires sudo). Uses the same layout as the distro packages:
#   /usr/bin/nexus-open
#   /usr/lib/nexus-open/plugins/
#   /usr/lib/nexus-open/ui-bundle/   (ui wrapper + ui.real for XWayland)
#   /usr/lib/systemd/user/nexus-open.service
#   /usr/share/nexus-open/configs/   (layout YAML for first-run seeding)
#   /usr/share/applications/nexus-open.desktop
#   /usr/share/icons/hicolor/        (app icons)
#   /usr/lib/udev/rules.d/99-corsair-nexus.rules
SYS_BIN       := /usr/bin
SYS_LIB       := /usr/lib/nexus-open
SYS_UNIT      := /usr/lib/systemd/user/nexus-open.service
SYS_DESKTOP   := /usr/share/applications/nexus-open.desktop
SYS_UDEV      := /usr/lib/udev/rules.d/99-corsair-nexus.rules

install: build-release build-ui build-plugins
	@echo "Stopping service..."
	@systemctl --user stop nexus-open.service 2>/dev/null || true
	@echo "Installing binary to $(SYS_BIN)..."
	@sudo install -Dm755 $(BIN_DIR)/$(APP_NAME) $(SYS_BIN)/$(APP_NAME)
	@echo "Installing plugins to $(SYS_LIB)/plugins..."
	@sudo mkdir -p $(SYS_LIB)/plugins
	@for mod in cpu-temp gpu-temp network weather cpu-load gpu-load media prometheus; do \
		if [ -f $(BIN_DIR)/plugins/nexus-$$mod ]; then \
			sudo install -Dm755 $(BIN_DIR)/plugins/nexus-$$mod $(SYS_LIB)/plugins/nexus-$$mod; \
		fi; \
	done
	@echo "Installing Flutter UI bundle to $(SYS_LIB)/ui-bundle..."
	@sudo mkdir -p $(SYS_LIB)/ui-bundle
	@sudo cp -r $(UI_BUILD_DIR)/. $(SYS_LIB)/ui-bundle/
	@sudo mv $(SYS_LIB)/ui-bundle/ui $(SYS_LIB)/ui-bundle/ui.real
	@printf '#!/usr/bin/env bash\nexec env WAYLAND_DISPLAY= "$$(dirname "$$0")/ui.real" "$$@"\n' | sudo tee $(SYS_LIB)/ui-bundle/ui > /dev/null
	@sudo chmod 755 $(SYS_LIB)/ui-bundle/ui $(SYS_LIB)/ui-bundle/ui.real
	@echo "Installing layout configs to /usr/share/nexus-open/..."
	@sudo mkdir -p /usr/share/nexus-open
	@sudo cp -r configs/. /usr/share/nexus-open/
	@echo "Installing icons..."
	@for size in 16 22 32 48 64 128 256; do \
		if [ -f internal/tray/icon-$$size.png ]; then \
			sudo install -Dm644 internal/tray/icon-$$size.png \
				/usr/share/icons/hicolor/$${size}x$${size}/apps/nexus-open.png; \
		fi; \
	done
	@command -v gtk-update-icon-cache > /dev/null 2>&1 && sudo gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
	@echo "Installing systemd unit..."
	@sudo install -Dm644 packaging/systemd/nexus-open.service $(SYS_UNIT)
	@sudo systemctl daemon-reload 2>/dev/null || true
	@echo "Installing desktop entry..."
	@sudo install -Dm644 packaging/desktop/nexus-open.desktop $(SYS_DESKTOP)
	@echo "Installing udev rule..."
	@sudo install -Dm644 packaging/udev/99-corsair-nexus.rules $(SYS_UDEV)
	@sudo udevadm control --reload-rules 2>/dev/null || true
	@echo "Restarting service..."
	@systemctl --user enable --now nexus-open.service
	@echo "✓ Installed and restarted"

# Uninstall system-wide install (requires sudo).
uninstall:
	@systemctl --user stop    nexus-open.service 2>/dev/null || true
	@systemctl --user disable nexus-open.service 2>/dev/null || true
	@sudo rm -f  $(SYS_BIN)/$(APP_NAME)
	@sudo rm -rf $(SYS_LIB)
	@sudo rm -f  $(SYS_UNIT)
	@sudo rm -f  $(SYS_DESKTOP)
	@sudo rm -f  $(SYS_UDEV)
	@sudo rm -rf /usr/share/nexus-open
	@for size in 16 22 32 48 64 128 256; do \
		sudo rm -f /usr/share/icons/hicolor/$${size}x$${size}/apps/nexus-open.png; \
	done
	@command -v gtk-update-icon-cache > /dev/null 2>&1 && sudo gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
	@sudo systemctl daemon-reload 2>/dev/null || true
	@sudo udevadm control --reload-rules 2>/dev/null || true
	@echo "✓ Uninstalled"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BIN_DIR) $(BUILD_DIR) $(DIST_DIR) plugins-dist
	@rm -f coverage.out coverage.html
	@rm -f $(APP_NAME) $(APP_NAME)-*
	@rm -rf $(BIN_DIR)/plugins
	@echo "✓ Cleaned"

# Clean Flutter UI build artifacts
clean-ui:
	@echo "Cleaning Flutter UI build artifacts..."
	@rm -rf $(UI_DIR)/build
	@echo "✓ Flutter UI cleaned"

# Additional development targets
.PHONY: fmt lint vet tidy generate-store

# Format code
fmt:
	@echo "Formatting code..."
	@go fmt ./...
	@echo "✓ Formatted"

# Run linter (requires golangci-lint)
lint:
	@if command -v golangci-lint > /dev/null; then \
		echo "Running linter..."; \
		golangci-lint run; \
	else \
		echo "Warning: golangci-lint not found. Install $(GOLANGCI_VERSION) from https://golangci-lint.run/"; \
	fi

# Run go vet
vet:
	@echo "Running go vet..."
	@go vet ./...
	@echo "✓ No issues found"

# Generate or preview CHANGELOG from git history
# Usage:
#   make changelog          — update CHANGELOG.md in place
#   make changelog TAG=v0.1.0  — preview notes for a specific (future) tag
changelog:
	@if ! command -v git-cliff > /dev/null; then \
		echo "git-cliff not found — install with: cargo install git-cliff"; exit 1; \
	fi
	@if [ -n "$(TAG)" ]; then \
		echo "Previewing release notes for $(TAG)..."; \
		git-cliff --tag "$(TAG)" --unreleased --strip all; \
	else \
		echo "Updating CHANGELOG.md..."; \
		git-cliff -o CHANGELOG.md; \
		echo "✓ CHANGELOG.md updated"; \
	fi

# Publish a new release.
# Usage: make release VERSION=0.3.0
#
# What it does (in order — stops on any error):
#   1. Validates the version argument and that the tree is clean
#   2. Updates CHANGELOG.md via git-cliff
#   3. Bumps pkgver/pkgrel in packaging/arch/PKGBUILD and packaging/rpm/nexus-open.spec
#   4. Commits the release files
#   5. Creates an annotated git tag
#   6. Pushes the commit and tag to origin
#
# CI takes over from there: builds packages, creates the GitHub release with
# all assets attached, then publishes to AUR. Do not create the GitHub release
# manually — the AUR workflow fires on release:published and requires the
# tarball to already be attached.
release:
	@if [ -z "$(VERSION)" ]; then \
		echo "Usage: make release VERSION=0.3.0"; exit 1; \
	fi
	@echo "$(VERSION)" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$$' \
		|| { echo "VERSION must be X.Y.Z (no leading v)"; exit 1; }
	@if ! command -v git-cliff > /dev/null; then \
		echo "git-cliff not found — install with: cargo install git-cliff"; exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "Working tree is not clean — commit or stash changes first"; exit 1; \
	fi
	@echo "--- Updating CHANGELOG.md for v$(VERSION)..."
	@git-cliff --tag "v$(VERSION)" -o CHANGELOG.md
	@echo "--- Bumping packaging versions to $(VERSION)..."
	@sed -i "s/^pkgver=.*/pkgver=$(VERSION)/" packaging/arch/PKGBUILD
	@sed -i "s/^pkgrel=.*/pkgrel=1/" packaging/arch/PKGBUILD
	@sed -i "s/^Version:.*/Version:        $(VERSION)/" packaging/rpm/nexus-open.spec
	@echo "--- Committing release files..."
	@git add CHANGELOG.md packaging/arch/PKGBUILD packaging/rpm/nexus-open.spec
	@git commit -m "release: v$(VERSION)"
	@echo "--- Tagging v$(VERSION)..."
	@git tag -a "v$(VERSION)" -m "v$(VERSION)"
	@echo "--- Pushing to origin..."
	@git push origin main
	@git push origin "v$(VERSION)"
	@echo ""
	@echo "✓ v$(VERSION) tagged and pushed — CI will build packages and publish the release."
	@echo "  Watch progress: https://github.com/mantonx/nexus-open/actions"

# Regenerate type-safe DB query code from schema.sql + queries/*.sql
generate-store:
	@if command -v sqlc > /dev/null || [ -f ~/go/bin/sqlc ]; then \
		SQLC=$$(command -v sqlc 2>/dev/null || echo ~/go/bin/sqlc); \
		INSTALLED=$$($$SQLC version 2>/dev/null); \
		if [ "$$INSTALLED" != "$(SQLC_VERSION)" ]; then \
			echo "Warning: sqlc $$INSTALLED installed, expected $(SQLC_VERSION)"; \
			echo "         Run: go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)"; \
		fi; \
		echo "Regenerating store query code..."; \
		$$SQLC generate; \
		echo "✓ internal/store/db/ regenerated"; \
	else \
		echo "Error: sqlc not found. Install with: go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)"; \
		exit 1; \
	fi

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	@go mod tidy
	@echo "✓ Dependencies tidied"

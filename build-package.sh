#!/usr/bin/env bash
# build-package.sh — build installable packages for all supported formats
#
# Usage:
#   ./build-package.sh                  # build deb + rpm + pacman + appimage
#   ./build-package.sh --deb            # deb only
#   ./build-package.sh --rpm            # rpm only
#   ./build-package.sh --pacman         # pacman only
#   ./build-package.sh --appimage       # AppImage only
#   ./build-package.sh --test           # build then smoke-test each in Docker
#
# Prerequisites (host):
#   - go 1.25+
#   - fpm (gem install fpm)
#   - appimagetool in PATH or ~/bin (for --appimage)
#   - rpm-build (for --rpm on non-RPM hosts): sudo pacman -S rpm-tools
#   - docker (for --test)
#
# The Flutter UI bundle is included if already built:
#   cd ui && flutter build linux --release --dart-define=APP_VERSION=$(git describe --tags --match 'v*' --always | sed 's/^v//')

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_DIR"

# ── config ────────────────────────────────────────────────────────────────────

PKG_NAME="nexus-open"
if [[ -n "${RELEASE_VERSION:-}" ]]; then
  # Caller (e.g. CI release workflow) supplies the exact version so that
  # commits landing after the tag don't produce a 'v0.2.0-3-gSHA' artifact name.
  PKG_VERSION="${RELEASE_VERSION#v}"
else
  _DIRTY_FLAG="--dirty"
  [[ "${CI:-}" == "true" ]] && _DIRTY_FLAG=""
  PKG_VERSION="$(git describe --tags --match 'v*' --always ${_DIRTY_FLAG} 2>/dev/null | sed 's/^v//' || echo "0.0.0-dev")"
  unset _DIRTY_FLAG
fi
PKG_ARCH="amd64"
PKG_DESCRIPTION="Linux controller for Corsair iCUE Nexus display"
PKG_URL="https://github.com/mantonx/nexus-open"
PKG_MAINTAINER="Matt <matthew.panton@gmail.com>"
PKG_LICENSE="MIT"

OUT_DIR="$REPO_DIR/dist"
STAGING_DIR="$REPO_DIR/.pkg-staging"

DAEMON_BIN="$REPO_DIR/nexus-open"
UI_BUNDLE="$REPO_DIR/ui/build/linux/x64/release/bundle"
ICON_DIR="$REPO_DIR/internal/tray"
UDEV_RULES="$REPO_DIR/packaging/udev/99-corsair-nexus.rules"
DESKTOP_FILE="$REPO_DIR/packaging/desktop/nexus-open.desktop"
SERVICE_FILE="$REPO_DIR/packaging/systemd/nexus-open.service"
POST_INSTALL="$REPO_DIR/packaging/scripts/post-install"
POST_UPGRADE="$REPO_DIR/packaging/scripts/post-upgrade"
PRE_REMOVE="$REPO_DIR/packaging/scripts/pre-remove"

# ── helpers ───────────────────────────────────────────────────────────────────

info()  { echo "  $*"; }
ok()    { echo "✓ $*"; }
warn()  { echo "! $*" >&2; }
die()   { echo "✗ $*" >&2; exit 1; }

# ── build Go binary ───────────────────────────────────────────────────────────

build_binary() {
    info "Building Go daemon (native)..."
    COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')
    go build \
        -trimpath \
        -ldflags "-X main.version=${PKG_VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
        -o "$DAEMON_BIN" \
        ./cmd/nexus-open
    ok "Built: $DAEMON_BIN"
}

# Build a fully static binary with CGO_ENABLED=0. No Docker needed — pure Go
# means no glibc dependency at all, so the binary runs on any Linux amd64.
# Also writes dist/nexus-open-<version>-linux-amd64.tar.gz for direct download.
build_binary_static() {
    info "Building static Go daemon (CGO_ENABLED=0)..."
    COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S')
    CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${PKG_VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
        -o "$DAEMON_BIN" \
        ./cmd/nexus-open
    ok "Built: $DAEMON_BIN (static)"

    # Build plugins into plugins/ alongside the daemon binary.
    # These are included in the tarball so AUR and direct installs get them.
    info "Building plugins for tarball..."
    mkdir -p "$REPO_DIR/plugins-dist"
    for mod in cpu-temp gpu-temp network weather cpu-load gpu-load media prometheus; do
        if [[ ! -d "$REPO_DIR/plugins/$mod" ]]; then continue; fi
        _ldf="-trimpath -ldflags \"-s -w\""
        if [[ "$mod" == "media" && -n "${TMDB_TOKEN:-}" ]]; then
            _hex="$("$REPO_DIR/scripts/xor-token.sh" "${TMDB_TOKEN}")"
            _ldf="-trimpath -ldflags \"-s -w -X main.bundledTMDbTokenHex=${_hex}\""
        fi
        eval "(cd \"$REPO_DIR/plugins/$mod\" && go build $_ldf -o \"$REPO_DIR/plugins-dist/nexus-$mod\" .)" \
            && ok "  Built plugin: nexus-$mod" \
            || warn "  Failed to build plugin: $mod"
    done

    # Produce a standalone tarball for direct installs and AUR.
    # Includes the daemon, plugins, Flutter UI bundle, and packaging files.
    mkdir -p "$OUT_DIR"
    local tarball="$OUT_DIR/${PKG_NAME}-${PKG_VERSION}-linux-amd64.tar.gz"
    local tar_root
    tar_root=$(mktemp -d)
    local pkg_dir="$tar_root/nexus-open-${PKG_VERSION}"
    mkdir -p "$pkg_dir"

    cp "$REPO_DIR/nexus-open" "$pkg_dir/"
    cp -r "$REPO_DIR/plugins-dist" "$pkg_dir/"
    cp -r "$REPO_DIR/packaging" "$pkg_dir/"
    cp "$REPO_DIR/LICENSE" "$pkg_dir/"

    if [[ -d "$UI_BUNDLE" ]]; then
        cp -r "$UI_BUNDLE" "$pkg_dir/ui-bundle"
        # Rename Flutter binary and add XWayland wrapper
        mv "$pkg_dir/ui-bundle/ui" "$pkg_dir/ui-bundle/ui.real"
        printf '#!/usr/bin/env bash\nexec env WAYLAND_DISPLAY= "$(dirname "$0")/ui.real" "$@"\n' \
            > "$pkg_dir/ui-bundle/ui"
        chmod +x "$pkg_dir/ui-bundle/ui"
        ok "Including Flutter UI bundle in tarball"
    else
        warn "Flutter UI not built — tarball will be daemon-only"
    fi

    tar -czf "$tarball" -C "$tar_root" "nexus-open-${PKG_VERSION}"
    rm -rf "$tar_root"
    ok "Static tarball: $tarball"
}

# ── staging area ──────────────────────────────────────────────────────────────

# Populate a staging tree that fpm maps into the package.
# Returns an array of "localpath=/destpath" fpm arguments via stdout.
build_staging() {
    rm -rf "$STAGING_DIR"

    # Daemon binary
    install -Dm755 "$DAEMON_BIN" "$STAGING_DIR/usr/bin/nexus-open"

    # Plugins — copy from plugins-dist/ built by build_binary_static
    info "Staging plugins..."
    mkdir -p "$STAGING_DIR/usr/lib/nexus-open/plugins"
    for f in "$REPO_DIR/plugins-dist"/nexus-*; do
        [[ -f "$f" ]] || continue
        install -m755 "$f" "$STAGING_DIR/usr/lib/nexus-open/plugins/"
        ok "  Staged plugin: $(basename "$f")"
    done

    # Flutter UI bundle (optional)
    if [[ -d "$UI_BUNDLE" ]]; then
        mkdir -p "$STAGING_DIR/usr/lib/nexus-open/ui-bundle"
        cp -r "$UI_BUNDLE/." "$STAGING_DIR/usr/lib/nexus-open/ui-bundle/"
        # Wrap the Flutter binary so it forces X11/XWayland — the GTK3 embedder
        # crashes on native Wayland sockets.
        mv "$STAGING_DIR/usr/lib/nexus-open/ui-bundle/ui" \
           "$STAGING_DIR/usr/lib/nexus-open/ui-bundle/ui.real"
        cat > "$STAGING_DIR/usr/lib/nexus-open/ui-bundle/ui" << 'WRAPPER'
#!/usr/bin/env bash
exec env WAYLAND_DISPLAY= "$(dirname "$0")/ui.real" "$@"
WRAPPER
        chmod +x "$STAGING_DIR/usr/lib/nexus-open/ui-bundle/ui"
        ok "Included Flutter UI bundle"
    else
        warn "Flutter UI not built — package will be daemon-only"
        warn "Build with: cd ui && flutter build linux --release"
    fi

    # Layout configs (fallback YAML for first-run zone seeding)
    if [[ -d "$REPO_DIR/configs" ]]; then
        mkdir -p "$STAGING_DIR/usr/share/nexus-open"
        cp -r "$REPO_DIR/configs" "$STAGING_DIR/usr/share/nexus-open/"
        ok "Included layout configs"
    fi

    # systemd user service
    install -Dm644 "$SERVICE_FILE" \
        "$STAGING_DIR/usr/lib/systemd/user/nexus-open.service"

    # Desktop file
    install -Dm644 "$DESKTOP_FILE" \
        "$STAGING_DIR/usr/share/applications/nexus-open.desktop"

    # udev rules
    install -Dm644 "$UDEV_RULES" \
        "$STAGING_DIR/usr/lib/udev/rules.d/99-corsair-nexus.rules"

    # Icons
    for size in 16 22 32 48 64 128 256; do
        src="$ICON_DIR/icon-${size}.png"
        if [[ -f "$src" ]]; then
            install -Dm644 "$src" \
                "$STAGING_DIR/usr/share/icons/hicolor/${size}x${size}/apps/nexus-open.png"
        fi
    done
}

# ── common fpm args ───────────────────────────────────────────────────────────

fpm_common() {
    local target="$1"
    "$FPM" \
        -s dir \
        -t "$target" \
        -n "$PKG_NAME" \
        -v "$PKG_VERSION" \
        --architecture "$PKG_ARCH" \
        --description "$PKG_DESCRIPTION" \
        --url "$PKG_URL" \
        --maintainer "$PKG_MAINTAINER" \
        --license "$PKG_LICENSE" \
        --category "utils" \
        --vendor "Nexus Open" \
        --chdir "$STAGING_DIR" \
        --package "$OUT_DIR/" \
        "${@:2}" \
        .
}

# ── deb ───────────────────────────────────────────────────────────────────────

build_deb() {
    info "Building .deb..."
    mkdir -p "$OUT_DIR"
    fpm_common deb \
        --depends "libusb-1.0-0 (>= 2:1.0.21)" \
        --depends "libayatana-appindicator3-1" \
        --depends "libgtk-3-0" \
        --depends "libgl1" \
        --depends "libegl1" \
        --depends "libgles2" \
        --after-install  "$POST_INSTALL" \
        --after-upgrade  "$POST_UPGRADE" \
        --before-remove  "$PRE_REMOVE"
    DEB_FILE=$(ls -t "$OUT_DIR"/${PKG_NAME}_*.deb 2>/dev/null | head -1)
    ok "Built: $DEB_FILE"
}

# ── rpm ───────────────────────────────────────────────────────────────────────

build_rpm() {
    command -v rpmbuild &>/dev/null || die "rpmbuild not found — sudo pacman -S rpm-tools"
    info "Building .rpm..."
    mkdir -p "$OUT_DIR"
    fpm_common rpm \
        --depends "libusb1" \
        --depends "libayatana-appindicator3" \
        --depends "gtk3" \
        --depends "mesa-libGL" \
        --depends "mesa-libEGL" \
        --depends "mesa-libGLES" \
        --rpm-summary "$PKG_DESCRIPTION" \
        --after-install  "$POST_INSTALL" \
        --after-upgrade  "$POST_UPGRADE" \
        --before-remove  "$PRE_REMOVE"
    RPM_FILE=$(ls -t "$OUT_DIR"/${PKG_NAME}-*.rpm 2>/dev/null | head -1)
    ok "Built: $RPM_FILE"
}

# ── pacman ────────────────────────────────────────────────────────────────────

build_pacman() {
    info "Building .pacman..."
    mkdir -p "$OUT_DIR"
    # Pacman versions cannot contain hyphens; replace with underscores for dev builds.
    local saved_ver="$PKG_VERSION"
    PKG_VERSION="${PKG_VERSION//-/_}"
    fpm_common pacman \
        --depends "libusb" \
        --depends "libayatana-appindicator" \
        --depends "gtk3" \
        --depends "mesa" \
        --after-install  "$POST_INSTALL" \
        --after-upgrade  "$POST_UPGRADE" \
        --before-remove  "$PRE_REMOVE"
    PKG_VERSION="$saved_ver"
    PACMAN_FILE=$(ls -t "$OUT_DIR"/${PKG_NAME}-*.pkg.tar.* 2>/dev/null | head -1)
    ok "Built: $PACMAN_FILE"
}

# ── appimage ──────────────────────────────────────────────────────────────────

build_appimage() {
    info "Building AppImage..."
    bash "$REPO_DIR/scripts/build-appimage.sh"
    APPIMAGE_FILE=$(ls -t "$OUT_DIR"/${PKG_NAME}-*.AppImage 2>/dev/null | head -1)
    ok "Built: $APPIMAGE_FILE"
}

# ── docker test ───────────────────────────────────────────────────────────────

# run_runtime_test IMAGE INSTALL_BLOCK FLUTTER_DEPS_BLOCK
# Shared test body: install package, verify daemon API, verify Flutter Xvfb.
# INSTALL_BLOCK       — shell snippet to install the package
# FLUTTER_DEPS_BLOCK  — shell snippet to install Xvfb + Mesa deps
# UNINSTALL_BLOCK     — shell snippet to remove the package
# SCREENSHOT_PKG      — package name that provides scrot (for screenshot)
# LABEL               — distro label used in screenshot filename
run_runtime_test() {
    local image="$1" install_block="$2" flutter_deps_block="$3"
    local uninstall_block="$4" screenshot_pkg="$5" label="$6"
    local ui_path="/usr/lib/nexus-open/ui-bundle/ui"
    local shots_dir="$REPO_DIR/test-screenshots"
    mkdir -p "$shots_dir"

    docker run --rm \
        -v "$OUT_DIR:/pkgs:ro" \
        -v "$shots_dir:/screenshots" \
        "$image" bash -c "
            set -e

            # ── install ──────────────────────────────────────────────────────
            ${install_block}

            # ── binary sanity ────────────────────────────────────────────────
            ldd /usr/bin/nexus-open | grep 'not found' \
                && { echo 'MISSING_LIBS'; exit 1; } || echo 'LIBS_OK'
            VERSION_OUT=\$(/usr/bin/nexus-open --version 2>&1)
            echo \"\$VERSION_OUT\" | grep -qE 'v[0-9]+\.[0-9]+\.[0-9]+' \
                && echo 'VERSION_OK' || { echo \"VERSION_BAD: \$VERSION_OUT\"; exit 1; }

            # ── packaging files present ───────────────────────────────────────
            test -f /usr/lib/udev/rules.d/99-corsair-nexus.rules \
                && echo 'UDEV_RULES_OK' || { echo 'UDEV_RULES_MISSING'; exit 1; }
            grep -q 'corsair\|CORSAIR\|1b1c' /usr/lib/udev/rules.d/99-corsair-nexus.rules \
                && echo 'UDEV_RULES_CONTENT_OK' || { echo 'UDEV_RULES_CONTENT_BAD'; exit 1; }
            test -f /usr/share/applications/nexus-open.desktop \
                && echo 'DESKTOP_FILE_OK' || { echo 'DESKTOP_FILE_MISSING'; exit 1; }
            test -f /usr/lib/systemd/user/nexus-open.service \
                && echo 'SYSTEMD_SERVICE_OK' || { echo 'SYSTEMD_SERVICE_MISSING'; exit 1; }

            # ── Flutter UI bundle layout ──────────────────────────────────────
            test -f /usr/lib/nexus-open/ui-bundle/ui \
                && echo 'UI_WRAPPER_OK' || { echo 'UI_WRAPPER_MISSING'; exit 1; }
            test -f /usr/lib/nexus-open/ui-bundle/ui.real \
                && echo 'UI_REAL_OK' || { echo 'UI_REAL_MISSING'; exit 1; }
            grep -q 'WAYLAND_DISPLAY=' /usr/lib/nexus-open/ui-bundle/ui \
                && echo 'UI_WRAPPER_CONTENT_OK' || { echo 'UI_WRAPPER_CONTENT_BAD: missing WAYLAND_DISPLAY= line'; exit 1; }
            # Check ELF magic bytes (\x7fELF) without requiring the 'file' command.
            head -c4 /usr/lib/nexus-open/ui-bundle/ui.real | grep -qP '^\x7fELF' \
                && echo 'UI_REAL_ELF_OK' || { echo 'UI_REAL_NOT_ELF'; exit 1; }

            # ── layout configs ────────────────────────────────────────────────
            test -d /usr/share/nexus-open/configs \
                && echo 'CONFIGS_OK' || { echo 'CONFIGS_MISSING'; exit 1; }

            # ── icons (at least one hicolor size) ─────────────────────────────
            ICON_FOUND=0
            for size in 16 22 32 48 64 128 256; do
                if [ -f \"/usr/share/icons/hicolor/\${size}x\${size}/apps/nexus-open.png\" ]; then
                    ICON_FOUND=1; break
                fi
            done
            [ \"\$ICON_FOUND\" -eq 1 ] \
                && echo 'ICONS_OK' || { echo 'ICONS_MISSING'; exit 1; }

            # ── all plugins present and executable ────────────────────────────
            PLUGIN_FAIL=0
            for p in nexus-cpu-temp nexus-gpu-temp nexus-network nexus-weather nexus-cpu-load nexus-gpu-load nexus-media; do
                path=\"/usr/lib/nexus-open/plugins/\$p\"
                if [ ! -f \"\$path\" ]; then
                    echo \"PLUGIN_MISSING: \$p\"; PLUGIN_FAIL=1
                elif [ ! -x \"\$path\" ]; then
                    echo \"PLUGIN_NOT_EXECUTABLE: \$p\"; PLUGIN_FAIL=1
                else
                    echo \"PLUGIN_OK: \$p\"
                fi
            done
            [ \"\$PLUGIN_FAIL\" -eq 0 ] || exit 1

            # ── daemon API (mock device, headless) ───────────────────────────
            NEXUS_MOCK_DEVICE=1 /usr/bin/nexus-open &
            DAEMON_PID=\$!
            for i in \$(seq 1 15); do
                curl -sf http://localhost:1985/api/health 2>/dev/null | grep -q '\"status\":\"ok\"' && break
                sleep 1
            done
            curl -sf http://localhost:1985/api/health | grep -q '\"status\":\"ok\"' \
                && echo 'API_HEALTH_OK' || { echo 'API_HEALTH_FAILED'; kill \$DAEMON_PID 2>/dev/null; exit 1; }

            # Read the auth token the daemon wrote on startup
            TOKEN=\$(cat /root/.config/nexus-open/token 2>/dev/null || echo '')
            [ -n \"\$TOKEN\" ] \
                && echo 'API_TOKEN_OK' || { echo 'API_TOKEN_MISSING'; kill \$DAEMON_PID 2>/dev/null; exit 1; }
            AUTH=\"X-Nexus-Token: \$TOKEN\"

            curl -sf -H \"\$AUTH\" http://localhost:1985/api/config | grep -q 'background_color' \
                && echo 'API_CONFIG_OK' || { echo 'API_CONFIG_FAILED'; kill \$DAEMON_PID 2>/dev/null; exit 1; }
            curl -sf -H \"\$AUTH\" http://localhost:1985/api/device/info | grep -q 'firmware' \
                && echo 'API_DEVICE_OK' || { echo 'API_DEVICE_FAILED'; kill \$DAEMON_PID 2>/dev/null; exit 1; }

            # ── plugins discoverable via catalog ──────────────────────────────
            # /api/plugins returns []CatalogEntry; exec plugins appear as
            # {"id":"exec:nexus-NAME","kind":"exec",...}. Give the daemon a
            # moment to scan the plugins dir before querying.
            sleep 2
            CATALOG=\$(curl -sf -H \"\$AUTH\" http://localhost:1985/api/plugins 2>/dev/null || echo '')
            [ -n \"\$CATALOG\" ] \
                && echo 'API_PLUGINS_RESPONDED' || { echo 'API_PLUGINS_FAILED'; kill \$DAEMON_PID 2>/dev/null; exit 1; }
            CATALOG_FAIL=0
            for p in nexus-cpu-temp nexus-gpu-temp nexus-network nexus-weather nexus-cpu-load nexus-gpu-load nexus-media; do
                echo \"\$CATALOG\" | grep -q \"exec:\$p\" \
                    && echo \"CATALOG_OK: \$p\" \
                    || { echo \"CATALOG_MISSING: \$p\"; CATALOG_FAIL=1; }
            done
            [ \"\$CATALOG_FAIL\" -eq 0 ] || { echo \"\$CATALOG\"; kill \$DAEMON_PID 2>/dev/null; exit 1; }

            kill \$DAEMON_PID 2>/dev/null; wait \$DAEMON_PID 2>/dev/null || true

            # ── Flutter UI (Xvfb + Mesa llvmpipe + screenshot) ───────────────
            ${flutter_deps_block} 2>&1 | tail -2
            Xvfb :99 -screen 0 1280x800x24 &
            sleep 1
            DISPLAY=:99 LIBGL_ALWAYS_SOFTWARE=1 GALLIUM_DRIVER=llvmpipe \
            WAYLAND_DISPLAY= NEXUS_START_MINIMIZED=0 \
                ${ui_path} > /tmp/flutter.log 2>&1 &
            UI_PID=\$!
            sleep 8
            if kill -0 \$UI_PID 2>/dev/null; then
                echo 'FLUTTER_ALIVE'
                ${screenshot_pkg} 2>&1 | tail -1 || true
                if command -v scrot &>/dev/null; then
                    DISPLAY=:99 scrot /screenshots/${label}.png 2>/dev/null && echo 'SCREENSHOT_OK' || echo 'SCREENSHOT_FAILED'
                elif command -v import &>/dev/null; then
                    DISPLAY=:99 import -window root /screenshots/${label}.png 2>/dev/null && echo 'SCREENSHOT_OK' || echo 'SCREENSHOT_FAILED'
                else
                    echo 'SCREENSHOT_SKIPPED (no scrot or import)'
                fi
                kill \$UI_PID
            else
                echo 'FLUTTER_CRASHED'; tail -20 /tmp/flutter.log; exit 1
            fi

            # ── clean uninstall ──────────────────────────────────────────────
            ${uninstall_block}
            test -f /usr/bin/nexus-open \
                && { echo 'UNINSTALL_FAILED: daemon binary still present'; exit 1; } \
                || echo 'UNINSTALL_BINARY_OK'
            test -f /usr/lib/nexus-open/plugins/nexus-media \
                && { echo 'UNINSTALL_FAILED: plugins still present'; exit 1; } \
                || echo 'UNINSTALL_PLUGINS_OK'
            test -f /usr/lib/nexus-open/ui-bundle/ui \
                && { echo 'UNINSTALL_FAILED: UI bundle still present'; exit 1; } \
                || echo 'UNINSTALL_UI_OK'
            test -f /usr/lib/systemd/user/nexus-open.service \
                && { echo 'UNINSTALL_FAILED: service file still present'; exit 1; } \
                || echo 'UNINSTALL_SERVICE_OK'
            test -f /usr/lib/udev/rules.d/99-corsair-nexus.rules \
                && { echo 'UNINSTALL_FAILED: udev rule still present'; exit 1; } \
                || echo 'UNINSTALL_UDEV_OK'
        " 2>&1 | grep -Ev '^(>|Warning|.*keysym|.*xkbcomp|Errors from)'
}

test_deb() {
    local deb_file="$1"
    local deb_base; deb_base="$(basename "$deb_file")"

    # Ubuntu 22.04 and Debian 12 require the Flatpak — glib 2.74/2.72 < 2.75.
    for distro in ubuntu:24.04; do
        local label; label="$(echo "$distro" | tr ':/' '-')"
        info "Testing .deb in ${distro}..."
        run_runtime_test "$distro" \
            "apt-get update -qq
             apt-get install -y --no-install-recommends curl
             dpkg -i /pkgs/${deb_base} || apt-get install -y -f --no-install-recommends" \
            "apt-get install -y --no-install-recommends xvfb libgl1-mesa-dri libgl1 libegl-mesa0 libegl1 libgles2 scrot" \
            "apt-get purge -y nexus-open && apt-get autoremove -y" \
            "apt-get install -y --no-install-recommends scrot 2>/dev/null || true" \
            "$label" \
            && ok "${distro} .deb test passed" || warn "${distro} .deb test FAILED"
    done
}

test_rpm() {
    local rpm_file="$1"
    local rpm_base; rpm_base="$(basename "$rpm_file")"

    info "Testing .rpm in Fedora 40..."
    run_runtime_test "fedora:40" \
        "dnf install -y /pkgs/${rpm_base}" \
        "dnf install -y xorg-x11-server-Xvfb mesa-dri-drivers mesa-libGL mesa-libEGL mesa-libGLES scrot" \
        "dnf remove -y nexus-open" \
        "dnf install -y scrot 2>/dev/null || true" \
        "fedora-40" \
        && ok "Fedora 40 .rpm test passed" || warn "Fedora 40 .rpm test FAILED"
}

test_pacman() {
    local pkg_file="$1"
    local pkg_base; pkg_base="$(basename "$pkg_file")"

    info "Testing .pacman in Arch Linux..."
    run_runtime_test "archlinux:latest" \
        "pacman -Sy --noconfirm && pacman -U --noconfirm /pkgs/${pkg_base}" \
        "pacman -S --noconfirm xorg-server-xvfb mesa scrot" \
        "pacman -R --noconfirm nexus-open" \
        "pacman -S --noconfirm scrot 2>/dev/null || true" \
        "arch" \
        && ok "Arch .pacman test passed" || warn "Arch .pacman test FAILED"
}

# ── main ──────────────────────────────────────────────────────────────────────

BUILD_DEB=false
BUILD_RPM=false
BUILD_PACMAN=false
BUILD_APPIMAGE=false
RUN_TESTS=false
TEST_DEB_ONLY=false
TEST_RPM_ONLY=false
TEST_PACMAN_ONLY=false

# Default: build all
if [[ $# -eq 0 ]]; then
    BUILD_DEB=true; BUILD_RPM=true; BUILD_PACMAN=true; BUILD_APPIMAGE=true
fi

for arg in "$@"; do
    case "$arg" in
        --deb)        BUILD_DEB=true ;;
        --rpm)        BUILD_RPM=true ;;
        --pacman)     BUILD_PACMAN=true ;;
        --appimage)   BUILD_APPIMAGE=true ;;
        --test)       BUILD_DEB=true; BUILD_RPM=true; BUILD_PACMAN=true; BUILD_APPIMAGE=true; RUN_TESTS=true ;;
        # Test-only flags: skip build, run Docker test against existing dist/ packages.
        --test-deb)   TEST_DEB_ONLY=true ;;
        --test-rpm)   TEST_RPM_ONLY=true ;;
        --test-pacman) TEST_PACMAN_ONLY=true ;;
        *) die "Unknown option: $arg. Usage: ./build-package.sh [--deb] [--rpm] [--pacman] [--appimage] [--test] [--test-deb|--test-rpm|--test-pacman]" ;;
    esac
done

# ── test-only mode (CI: packages pre-built, just run Docker tests) ────────────
if $TEST_DEB_ONLY || $TEST_RPM_ONLY || $TEST_PACMAN_ONLY; then
    command -v docker &>/dev/null || die "docker not found"
    if $TEST_DEB_ONLY; then
        f=$(ls -t "$OUT_DIR"/nexus-open_*.deb 2>/dev/null | head -1)
        [[ -n "$f" ]] || die "No .deb found in $OUT_DIR/ — build first"
        test_deb "$f" && ok "deb test passed" || die "deb test FAILED"
    fi
    if $TEST_RPM_ONLY; then
        f=$(ls -t "$OUT_DIR"/nexus-open-*.rpm 2>/dev/null | head -1)
        [[ -n "$f" ]] || die "No .rpm found in $OUT_DIR/ — build first"
        test_rpm "$f" && ok "rpm test passed" || die "rpm test FAILED"
    fi
    if $TEST_PACMAN_ONLY; then
        f=$(ls -t "$OUT_DIR"/nexus-open-*.pkg.tar.* 2>/dev/null | head -1)
        [[ -n "$f" ]] || die "No .pkg.tar.* found in $OUT_DIR/ — build first"
        test_pacman "$f" && ok "pacman test passed" || die "pacman test FAILED"
    fi
    exit 0
fi

# ── build mode ────────────────────────────────────────────────────────────────
FPM="$HOME/.local/share/gem/ruby/3.4.0/bin/fpm"
[[ -x "$FPM" ]] || FPM="$(command -v fpm 2>/dev/null)" || { echo "fpm not found — gem install fpm"; exit 1; }

echo "Building Nexus Open v${PKG_VERSION} packages..."
echo ""

# Build binary and staging once — all formats share the same artifacts.
if $BUILD_DEB || $BUILD_RPM || $BUILD_PACMAN || $BUILD_APPIMAGE; then
    build_binary_static
    build_staging
    $BUILD_DEB    && build_deb
    $BUILD_RPM    && build_rpm
    $BUILD_PACMAN && build_pacman
    # AppImage delegates to its own script which manages its own AppDir.
    # Requires appimagetool in PATH or ~/bin.
    $BUILD_APPIMAGE && build_appimage
fi

if $RUN_TESTS; then
    echo ""
    echo "Running Docker install tests..."
    command -v docker &>/dev/null || die "docker not found — needed for --test"
    $BUILD_DEB    && [[ -n "${DEB_FILE:-}" ]]    && test_deb "$DEB_FILE"
    $BUILD_RPM    && [[ -n "${RPM_FILE:-}" ]]    && test_rpm "$RPM_FILE"
    $BUILD_PACMAN && [[ -n "${PACMAN_FILE:-}" ]] && test_pacman "$PACMAN_FILE"
fi

echo ""
ok "Done. Packages in $OUT_DIR/"
ls -lh "$OUT_DIR/"

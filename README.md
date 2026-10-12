# Nexus Open

Linux support for the Corsair iCUE Nexus, the 640×48 pixel display strip that
sits above your keyboard. Corsair doesn't make a Linux driver; this project
reverse-engineered the protocol and built one.

[![Release](https://img.shields.io/github/v/release/mantonx/nexus-open)](https://github.com/mantonx/nexus-open/releases/latest)
[![AUR](https://img.shields.io/aur/version/nexus-open)](https://aur.archlinux.org/packages/nexus-open)
[![CI](https://github.com/mantonx/nexus-open/actions/workflows/ci.yml/badge.svg)](https://github.com/mantonx/nexus-open/actions/workflows/ci.yml)
![Go](https://img.shields.io/github/go-mod/go-version/mantonx/nexus-open?color=00ADD8)
![Flutter](https://img.shields.io/badge/dynamic/yaml?url=https://raw.githubusercontent.com/mantonx/nexus-open/main/ui/pubspec.yaml&query=$.environment.flutter&label=flutter&color=02569B)
![Platform](https://img.shields.io/badge/platform-Linux-orange)
![License](https://img.shields.io/badge/license-MIT-blue)

## Screenshots

![640×48 hardware display showing clock, weather, CPU, GPU, and network zones](docs/screenshots/display.png)

![Settings app with layout editor and live hardware preview strip](docs/screenshots/settings-ui.png)

## What you get

Live stats on the display strip: CPU and GPU temperature and load, network
throughput, and weather from open-meteo. Swipe left and right to switch between
pages. Each zone is independently configurable: pick a plugin, choose a graph
style (sparkline, bar, area), and set the colours.

A settings app lets you rearrange the layout and swap plugins without restarting
anything. The preview shows the exact image going to the hardware in real time.

The daemon runs quietly as a systemd user service and starts automatically on
login. No Corsair software, no Wine, no background cloud process.

## Plugins

| Plugin    | What it shows                                  |
| --------- | ---------------------------------------------- |
| cpu-temp  | CPU package temperature                        |
| cpu-load  | Per-core or aggregate CPU utilisation          |
| gpu-temp  | GPU temperature (AMD, Intel, NVIDIA)           |
| gpu-load  | GPU utilisation                                |
| network   | Download and upload throughput                 |
| weather   | Current conditions from open-meteo             |
| media     | Now playing via MPRIS/playerctl                |
| clock     | Time and date (built in)                       |

You can also write your own. A plugin is a Go binary that implements a
three-method interface. Drop it anywhere and reference it in a YAML file.
See [CONTRIBUTING.md](CONTRIBUTING.md) for a walkthrough.

## Install

Download from the [releases page](https://github.com/mantonx/nexus-open/releases/latest):

| Format         | Distro                                              |
| -------------- | --------------------------------------------------- |
| `.pkg.tar.zst` | Arch Linux (or `yay -S nexus-open`)                 |
| `.deb`         | Ubuntu 24.04+, Debian 13+                           |
| `.rpm`         | Fedora 40+, RHEL 9+                                 |
| `.tar.gz`      | Any distro, static binary, no runtime dependencies  |

After installing, unplug and replug the Nexus. The bundled udev rule grants
access without adding your user to any group.

See [docs/INSTALLATION.md](docs/INSTALLATION.md) for details, or
[DEVICE_SETUP.md](DEVICE_SETUP.md) if you run into USB permission issues.

## Building from source

Requires Go 1.26+ and Flutter 3.24+. No C libraries needed.

```bash
git clone https://github.com/mantonx/nexus-open.git
cd nexus-open
make setup   # installs dev tools
make dev     # Go + Flutter with hot-reload
```

No hardware? `NEXUS_MOCK_DEVICE=1 make dev` works without the device plugged in.

See [DEVELOPMENT.md](DEVELOPMENT.md) for the full dev workflow.

## Known issues

The settings UI requires XWayland (handled automatically in the packaged binary).

## More

- [docs/INSTALLATION.md](docs/INSTALLATION.md) — install, build from source, configuration
- [DEVICE_SETUP.md](DEVICE_SETUP.md) — USB permissions and troubleshooting
- [DEVELOPMENT.md](DEVELOPMENT.md) — architecture, hot-reload workflow, testing
- [CONTRIBUTING.md](CONTRIBUTING.md) — writing plugins, submitting PRs
- [docs/USB_PROTOCOL.md](docs/USB_PROTOCOL.md) — reverse-engineered protocol, frame format, touch HID reports

## License

MIT. See [LICENSE](LICENSE).

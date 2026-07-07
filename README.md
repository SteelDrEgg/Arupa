<div align="center">

# Arupa

<img width="80%" src="doc/plugins.png" alt="Arupa plugin page" />

**A server management panel with a minimal kernel and a language-agnostic plugin system.**

[![Release](https://github.com/SteelDrEgg/Arupa/actions/workflows/release.yml/badge.svg?branch=main)](https://github.com/SteelDrEgg/Arupa/actions/workflows/release.yml)
[![CI](https://github.com/SteelDrEgg/Arupa/actions/workflows/ci.yml/badge.svg)](https://github.com/SteelDrEgg/Arupa/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)
[![Stars](https://img.shields.io/github/stars/SteelDrEgg/Arupa?style=flat-plastic)](https://github.com/SteelDrEgg/Arupa/stargazers)
![License](https://img.shields.io/badge/license-MIT-maroon)

</div>

---

Arupa is a lightweight server management panel written in Go. The kernel is deliberately tiny — it does little more than load and orchestrate plugins. 
**Every feature is a plugin**, and plugins can be written in **any language** that speaks the protocol.

## Highlights

- **Minimal kernel** — the core only loads and coordinates plugins; nothing else is baked in.
- **Polyglot plugins** — write them in Go, Python, Rust, or anything else you like.
- **Everything is a plugin** — even login, navigation, and plugin management ship as plugins.
- **Single binary** — drop-in executables for Linux and macOS, plus an official Docker image.

## Quick Start

```sh
# 1. Grab a binary from the latest release, then make it executable
chmod +x arupa-<version>-linux-amd64

# 2. Create an admin user
./arupa-<version>-linux-amd64 -config config.toml -user admin -password 'change-me'

# 3. Start the server
./arupa-<version>-linux-amd64 -config config.toml
```

Then open <http://localhost:8080>.

## Installation

### Binary

Download the build for your platform from the [latest release](https://github.com/SteelDrEgg/Arupa/releases/latest):

| Platform | Asset |
| --- | --- |
| Linux (x86-64) | `arupa-<version>-linux-amd64` |
| Linux (ARM64) | `arupa-<version>-linux-arm64` |
| macOS (Intel) | `arupa-<version>-darwin-amd64` |
| macOS (Apple Silicon) | `arupa-<version>-darwin-arm64` |

Make it executable and run it:

```sh
chmod +x arupa-<version>-linux-amd64
./arupa-<version>-linux-amd64 -config config.toml
```

### Docker

Pull the image:

```sh
docker pull ghcr.io/steeldregg/arupa:latest
```

Initialize an admin user in a persistent data volume:

```sh
docker run --rm -v arupa-data:/data \
  ghcr.io/steeldregg/arupa:latest \
  -config /data/config.toml -user admin -password 'change-me'
```

Start the server:

```sh
docker run -p 8080:8080 -v arupa-data:/data \
  ghcr.io/steeldregg/arupa:latest
```

## Usage

```sh
# Run Arupa with a config file
arupa -config config.toml

# Create or update a user
arupa -config config.toml -user admin -password 'change-me'

# Print the version
arupa -version
```

> The core plugins are **not bundled** with release and must be installed separately. Without them there is no web UI.
> The panel remains fully usable over the API, but you need the plugins below to get a GUI.
> See [plugins](#Plugins) for more information

## Configuration

Arupa reads `config.toml` by default. If the file does not exist, it is created with default values on first run.

<details>
<summary><b>Minimal <code>config.toml</code> example</b></summary>

```toml
Listen = ":8080"
PluginDir = "plugins"
PluginTempDir = "tmp"

[Users]
  admin = "<bcrypt-password-hash>"

[Plugins]
[Plugins.default]
Restart = "no"
RunAsUser = ""

# Core plugins
[Plugins.web-assets]
Restart = "always"
[Plugins.login]
Restart = "always"
[Plugins.navigator]
Restart = "always"
[Plugins.plugin-manager]
Restart = "always"
```

</details>

| Key | Description |
| --- | --- |
| `Listen` | HTTP listen address, e.g. `:8080`. |
| `PluginDir` | Directory containing `.plg` plugin packages. |
| `PluginTempDir` | Temporary directory used while loading plugins. |
| `[Users]` | Login users mapped to bcrypt password hashes. Manage them with `arupa -user <name> -password <password>`. |
| `[Plugins.<name>]` | Per-plugin settings such as `Restart` (`no` / `always`) and `RunAsUser`. |

## Plugins

Arupa's kernel does almost nothing on its own — the panel's features all come from plugins. A plugin is a `.plg` package placed in `PluginDir`; the kernel loads it on startup and (re)starts it according to its `Restart` policy.



### Core plugins

The default panel is built from four core plugins, maintained in [SteelDrEgg/coreplugins](https://github.com/SteelDrEgg/coreplugins). Grab them from the [latest release](https://github.com/SteelDrEgg/coreplugins/releases/latest):

| Plugin | Role                          |
| --- |-------------------------------|
| [`login`](https://github.com/SteelDrEgg/coreplugins/releases/latest/download/login.plg) | Login and Logout page         |
| [`navigator`](https://github.com/SteelDrEgg/coreplugins/releases/latest/download/navigator.plg) | Navigation across pages       |
| [`plugin-manager`](https://github.com/SteelDrEgg/coreplugins/releases/latest/download/plugin-manager.plg) | Manage plugins from the UI    |
| [`web-assets`](https://github.com/SteelDrEgg/coreplugins/releases/latest/download/web-assets.plg)  | Serves style and layout files |

Download all four into your `PluginDir`:

```sh
cd plugins   # your PluginDir
for p in login navigator plugin-manager web-assets; do
  curl -LO "https://github.com/SteelDrEgg/coreplugins/releases/latest/download/$p.plg"
done
```

The default behavior is not start a plugin unless explicitly been told to.
To change this behavior, edit `Plugins.default` in config file.
```toml
[Plugins.default]
Restart = "no"
```

Or set the behavior for each plugin.
```toml
[Plugins.<plugin>]
Restart = "always"
```

You can also start and stop a plugin via web ui after starting `plugin-manager`.

### Writing your own

Plugins talk to the kernel over protobuf3 [protocol](https://github.com/SteelDrEgg/Arupa/blob/main/proto/panel.proto), so you can write them in any language. 
Package your plugin as a `.plg`, drop it into `PluginDir`, and start it from the panel via `plugin-manager`.

This is still a very primitive project, a thorough documentation is on its way.

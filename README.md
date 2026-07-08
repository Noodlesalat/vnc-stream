# vnc-stream

`vnc-stream` is a lightweight Go tool that connects to a VNC server and distributes the live screen contents as an M-JPEG (Motion JPEG) stream over HTTP.

It features **graceful background auto-reconnection**, a **premium web-based live dashboard**, and **dynamic test card fallback**. When the VNC server is down or authenticating, the HTTP stream continues running and broadcasts a standard television test card (color bars) to keep viewers connected, switching back to the live feed automatically once the server is reachable.

---

## Features

- **Single VNC Connection Broker:** Connects to the VNC server once and broadcasts the frame updates to any number of HTTP subscribers. This avoids overloading the VNC host.
- **Graceful Reconnection:** Background thread handles VNC timeouts, socket closures, or bad authentication. It will periodically attempt to reconnect without terminating the HTTP server.
- **Television Test Card Fallback:** Dynamically streams colorful SMPTE-style color bars when VNC is disconnected.
- **Premium Live Dashboard:** Includes a clean, responsive, dark-themed dashboard showing VNC host info, target FPS, and live connection status.
- **Config & Secret Support:** Configuration can be parsed from a JSON file, enabling easy secret management on systems like NixOS (e.g., using sops-nix, agenix, or systemd credential systems).

---

## Installation

Ensure you have [Go](https://golang.org/) (v1.18+) installed:

```bash
go build -o vnc-stream main.go
```

---

## Usage

Start the server using CLI arguments:

```bash
./vnc-stream -host 192.168.1.50 -port 5900 -password mypassword -listen :8080 -fps 15 -quality 80
```

Open `http://localhost:8080/` in a web browser to access the dashboard.
Open `http://localhost:8080/stream` to access the raw M-JPEG stream (useful in external players or home automation platforms like Home Assistant).

### CLI Parameters

| Flag | Default | Description |
|------|---------|-------------|
| `-host` | `localhost` | Hostname or IP of the VNC server |
| `-port` | `5900` | Port of the VNC server |
| `-password` | *(empty)* | Password of the VNC server |
| `-listen` | `:8080` | HTTP listen address |
| `-fps` | `10` | Frame rate for the M-JPEG stream |
| `-quality` | `80` | JPEG compression quality (1-100) |
| `-bpp` | `32` | VNC color depth in bits per pixel (`8`, `16`, or `32`) |
| `-config` | *(empty)* | Path to JSON configuration file |

---

## Configuration File

You can store the configuration in a JSON file to separate credentials from your invocation commands. Any arguments specified on the command-line will override the settings read from the configuration file.

### Configuration File Schema (`config.json`)

```json
{
  "host": "192.168.1.50",
  "port": 5900,
  "password": "my-vnc-password",
  "listen": ":8080",
  "fps": 15,
  "quality": 85,
  "bpp": 16
}
```

Run the streamer:
```bash
./vnc-stream -config /path/to/config.json
```

---

## Bandwidth Optimization

If you need to minimize network traffic (e.g. for slow or remote connections), you can optimize bandwidth at both the VNC transport and HTTP streaming levels:

1. **VNC Server-to-Streamer Bandwidth (`-bpp`):**
   Setting `-bpp 16` drops the color depth from 32-bit (true color) to 16-bit. This halves the raw data size transferred between the VNC server and the streamer, greatly improving compression efficiency with no visible text-readability loss.
2. **Streamer-to-Browser Bandwidth (`-fps` and `-quality`):**
   - **Lower the FPS:** Reducing frame rate (e.g. `-fps 5` or `-fps 3`) drastically reduces HTTP stream throughput.
   - **Lower the JPEG Quality:** Dropping quality to `-quality 50` or `-quality 40` compresses the MJPEG frames significantly while keeping the visual output clean enough for server monitoring.

---

## NixOS Configuration

This project is fully compatible with Nix flakes and includes a built-in multi-instance NixOS module.

### 1. Add to flake inputs
```nix
inputs.vnc-stream.url = "github:Noodlesalat/vnc-stream";
```

### 2. Configure NixOS module options (Multi-Instance Example)
```nix
{ config, pkgs, inputs, ... }: {
  imports = [ inputs.vnc-stream.nixosModules.default ];

  services.vnc-stream = {
    enable = true;

    instances = {
      # First VNC Server stream (High Quality)
      desktop = {
        host = "192.168.1.100";
        port = 5900;
        listen = ":8080";
        fps = 15;
        quality = 80;
        bpp = 32;
        configFile = "/run/secrets/vnc-desktop-config.json"; # Contains VNC password
      };

      # Second VNC Server stream (Bandwidth-Optimized)
      server-console = {
        host = "192.168.1.101";
        port = 5900;
        listen = ":8081";
        fps = 5;
        quality = 50;
        bpp = 16; # Switch to 16-bit color to save VNC bandwidth
        configFile = "/run/secrets/vnc-console-config.json";
      };
    };
  };
}
```

Each instance dynamically starts its own Systemd service: `vnc-stream-desktop.service` and `vnc-stream-server-console.service`.

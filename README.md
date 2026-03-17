# On hold, since the batteries lost the connection to the energy meter

# marstek-venus-evcc

> **Early release** — working but lightly tested. Feedback and contributions welcome.

HTTP proxy that integrates **Marstek Venus E/C battery storage** with **[evcc](https://evcc.io)** using the Marstek local UDP API.

Marstek devices communicate via JSON-RPC over UDP on your local network. Since evcc uses HTTP plugins for custom batteries, this proxy bridges the two.

## Features

- Read SOC (state of charge) across one or multiple batteries
- Read battery power (aggregated, sign-corrected for evcc)
- Control charge / discharge / auto mode via evcc's `batterymode`
- Automatic discharge blocking below a configurable minimum SOC
- Supports multiple batteries (power summed, SOC capacity-weighted)

## Requirements

- Marstek Venus E or C battery with **Open API enabled** in the Marstek app
- [evcc](https://evcc.io) installed
- Go 1.22+ (only needed to build from source)

## Setup

### 1. Enable the Open API on your battery

In the Marstek app: open each device → Settings → **Open API** → turn on.

Note the **UDP port** shown (default: `30000`). All batteries should use the same port.

> Enabling the Open API may disable some built-in device features to prevent conflicts — see the Marstek API docs for details.

### 2. Find your battery IP addresses

Check your router's device list or the Marstek app for each battery's local IP address. Assigning **static IPs** (via DHCP reservation in your router) is strongly recommended so the addresses don't change.

### 3. Build

```bash
# Clone
git clone https://github.com/coltmans/marstek-venus-evcc.git
cd marstek-venus-evcc

# For Raspberry Pi (64-bit, aarch64)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o marstek-api-arm64 .

# For Raspberry Pi (32-bit, armv7)
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o marstek-api-armv7 .

# For local testing (Mac/Linux)
make build
```

### 4. Configure

Set your device IPs and port via environment variable:

```bash
MARSTEK_DEVICES=192.168.1.100,192.168.1.101 ./marstek-api
```

Or edit the defaults directly in `main.go`:

```go
var defaultDevices = []deviceConfig{
    {Host: "192.168.1.100", Port: 30000},
    {Host: "192.168.1.101", Port: 30000},
}
```

Other configurable constants in `main.go`:

| Constant | Default | Description |
|---|---|---|
| `listenAddr` | `:7071` | HTTP port for the proxy |
| `passiveCountdown` | `90` | Seconds before battery reverts to auto if evcc stops sending |
| `minSOC` | `11.0` | Minimum SOC % — discharge commands blocked below this |
| `defaultPowerPerDevice` | `2500` | Per-device power when evcc doesn't specify a wattage (W) |

> **Note:** Port 7071 is used because 7070 is evcc's default web UI port.

### 5. Install as a systemd service (Raspberry Pi)

Copy the binary to your Pi and install as a service:

```bash
sudo cp marstek-api-arm64 /usr/local/bin/marstek-api
sudo chmod +x /usr/local/bin/marstek-api

sudo nano /etc/systemd/system/marstek-api.service
```

```ini
[Unit]
Description=Marstek Battery API Proxy
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/marstek-api
Restart=always
RestartSec=10
Environment=MARSTEK_DEVICES=192.168.1.100,192.168.1.101

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable marstek-api
sudo systemctl start marstek-api
sudo systemctl status marstek-api
```

### 6. Add to evcc.yaml

Add the following to your `evcc.yaml` under the `batteries:` key (adjust capacity to match your setup — Venus E V2 is 5120 Wh per unit):

```yaml
batteries:
  - name: Marstek Venus
    type: custom

    soc:
      source: http
      uri: http://localhost:7071/api/soc
      jq: .soc

    power:
      source: http
      uri: http://localhost:7071/api/power
      jq: .power

    capacity: 5120  # Wh — multiply by number of batteries

    # Optional: allow evcc to control charge/discharge
    batterymode:
      source: http
      uri: http://localhost:7071/api/batterymode
      method: POST
      body: '{"mode": "{{.batteryMode}}"}'
      headers:
        - Content-Type: application/json
```

Restart evcc after editing.

## Discover devices

Use the included discovery tool to find Marstek devices on your network and confirm the API is reachable:

```bash
CGO_ENABLED=0 go run ./cmd/discover/
```

Example output:
```json
From 192.168.1.100:30000:
{
  "src": "VenusE-xxxxxxxxxxxx",
  "result": {
    "device": "VenusE",
    "ip": "192.168.1.100",
    "ver": 153
  }
}
```

## API Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/api/soc` | GET | `{"soc": 85.0}` — averaged SOC in % |
| `/api/power` | GET | `{"power": 1234.0}` — W, positive = charging |
| `/api/status` | GET | Full aggregated status |
| `/api/batterymode` | POST | `{"mode": "normal"\|"charge"\|"discharge"}` |

## Technical notes

### Protocol
The Marstek local API uses JSON-RPC 2.0 over UDP. One non-obvious requirement: the local UDP socket must bind to the **same port number** as the device's configured API port. The device responds to the source port of incoming packets — using a random ephemeral source port means responses are never received.

### Sign conventions
- `bat_power` from the device: positive = discharging (proxy inverts this for evcc)
- Passive mode power setpoint: negative = charge, positive = discharge

### Multiple batteries
Power is summed across all configured devices; SOC is capacity-weighted.

## Tested with

- Marstek Venus E V2 (firmware 153)
- evcc on Raspberry Pi 4 (aarch64)

## Related

- [evcc](https://github.com/evcc-io/evcc) — the EV charging controller this proxy integrates with
- [evcc docs: plugins](https://docs.evcc.io/docs/devices/plugins) — custom meter plugin documentation
- [jaapp/ha-marstek-local-api](https://github.com/jaapp/ha-marstek-local-api) — Home Assistant integration, helped with protocol research
- [Marstek Device Open API Rev 2.0](https://static-eu.marstekcloud.com/ems/resource/agreement/MarstekDeviceOpenApi.pdf) — official API documentation

# paseo-relay

[中文](README.zh.md) | English

A lightweight self-hosted relay server for [Paseo](https://github.com/getpaseo/paseo).

The relay bridges your Paseo daemon and the mobile app when they can't connect directly. It forwards encrypted bytes without being able to read them — the relay is completely untrusted by design.

## How it works

```
Mobile App  ──────────────────────────────────────┐
                                                   ▼
                                           [ paseo-relay ]
                                                   ▲
Paseo Daemon ─────────────────────────────────────┘
```

All traffic between daemon and app is E2E encrypted with XSalsa20-Poly1305 (NaCl box). The relay sees only IP addresses, timing, message sizes, and session IDs — never the content.

## Protocol (v2)

Three WebSocket endpoints under `GET /ws`:

| Role | Query params | Description |
|------|-------------|-------------|
| Daemon control | `role=server&v=2` | One per daemon session, receives connect/disconnect events |
| Daemon data | `role=server&connectionId=<c>&v=2` | One per client connection, forwards encrypted frames |
| Client | `role=client[&connectionId=<c>]&v=2` | App socket, multiple allowed per connectionId |

`GET /health` returns `{"status":"ok","version":"<version>"}`.

## Quick start

### Docker

```bash
docker run -p 8411:8411 ghcr.io/zenghongtu/paseo-relay:latest
```

### Binary (pre-built)

Download the binary for your platform from [Releases](https://github.com/zenghongtu/paseo-relay/releases):

| Platform | File |
|----------|------|
| Linux x86_64 | `paseo-relay-<version>-linux-amd64.tar.gz` |
| Linux ARM64 | `paseo-relay-<version>-linux-arm64.tar.gz` |
| macOS x86_64 | `paseo-relay-<version>-darwin-amd64.tar.gz` |
| macOS Apple Silicon | `paseo-relay-<version>-darwin-arm64.tar.gz` |
| Windows x86_64 | `paseo-relay-<version>-windows-amd64.zip` |

```bash
# Linux/macOS
curl -L https://github.com/zenghongtu/paseo-relay/releases/latest/download/paseo-relay-v0.3.0-linux-amd64.tar.gz | tar xz
./paseo-relay

# Windows: extract the .zip and run paseo-relay.exe
```

### Build from source

```bash
git clone https://github.com/zenghongtu/paseo-relay
cd paseo-relay
go build -o paseo-relay .
./paseo-relay
```

## Configuration

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--addr` | `RELAY_ADDR` | `:8411` | Listen address |
| `--max-buffer-frames` | — | `200` | Max frames buffered per connection while daemon is connecting |
| `--log-format` | `LOG_FORMAT` | `text` | Log format: `text` or `json` |
| `--version` | — | — | Print version and exit |

## Deployment

### systemd

```ini
[Unit]
Description=Paseo relay
After=network.target

[Service]
ExecStart=/usr/local/bin/paseo-relay --addr :8411 --log-format json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### Docker Compose

```yaml
services:
  relay:
    image: ghcr.io/zenghongtu/paseo-relay:latest
    restart: unless-stopped
    ports:
      - "8411:8411"
    environment:
      LOG_FORMAT: json
```

TLS should be terminated upstream (nginx, Caddy, Cloudflare, etc.).

### Configure Paseo daemon

In `~/.paseo/config.json`:

**With domain:**
```json
{
  "daemon": {
    "relay": {
      "enabled": true,
      "endpoint": "relay.yourdomain.com:443",
      "publicEndpoint": "relay.yourdomain.com:443"
    }
  }
}
```

**With IP address:**
```json
{
  "daemon": {
    "relay": {
      "enabled": true,
      "endpoint": "1.2.3.4:8411",
      "publicEndpoint": "1.2.3.4:8411"
    }
  }
}
```

Then restart the daemon: `paseo daemon stop && paseo daemon start`.

## Security

- The relay is zero-knowledge — it cannot read or forge messages.
- No authentication on the relay itself; security is enforced end-to-end by Paseo's ECDH + NaCl box encryption.
- Always terminate TLS upstream; do not expose the relay over plain HTTP.
- See [Paseo SECURITY.md](https://github.com/getpaseo/paseo/blob/main/SECURITY.md) for the full threat model.

## License

AGPLv3 — see [LICENSE](LICENSE).

# Changelog

All notable changes to paseo-relay are documented here.

## [v0.4.0] - 2026-04-11

### Security
- Enforce 10 MB per-message size limit on all WebSocket connections to prevent memory exhaustion DoS
- Cap concurrent sessions at 10,000 to prevent unbounded session creation attacks

## [v0.3.0] - 2026-04-11

### Changed
- Change default listen port from 8080 to 8411
- Add curl install commands for each platform binary in README

## [v0.2.1] - 2026-04-11

### Docs
- Add IP address config example alongside domain example in README

## [v0.2.0] - 2026-04-11

### Changed
- Replace `log` with `log/slog` for structured logging
- Upgrade Go version to 1.26

## [v0.1.0] - 2026-04-11

### Added
- Initial release: zero-knowledge WebSocket relay server for Paseo
- Protocol v2 with server control socket, server data socket, and client socket roles
- In-memory session registry with periodic eviction
- Frame buffering while daemon data socket connects
- Graceful shutdown support

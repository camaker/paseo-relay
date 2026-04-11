# Changelog

All notable changes to paseo-relay are documented here.

## [v0.5.0] - 2026-04-11

### Performance
- Reduce lock contention on forwarding hot-path: extract per-connectionId state into `pipe` struct with its own mutex; message forwarding no longer touches the session-level lock
- `connectedConnectionIds` now iterates pipes under a single lock instead of double-locking

### Fixed
- Fix race condition in `nudgeOrResetControl`: inner timer now re-acquires lock and compares control pointer before closing, preventing accidental close of a replaced connection

### Changed
- Session cleanup is now active (on handler exit) instead of relying solely on the 60s eviction ticker; eviction loop kept as a 5-minute backstop
- `frameBuffer` now enforces a 32 MB total byte limit in addition to the frame count limit, evicting oldest frames when the limit is reached
- Scope GitHub Actions release workflow permissions per job (least privilege)

### Security
- Remove weak time-based fallback in `randomHex`; `crypto/rand` failure now panics instead of silently degrading to predictable IDs

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

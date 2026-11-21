# xabi

Minimalist, zero-overhead local web development orchestrator running in Linux user-space.

Built in Go using only the standard library for instant startup (`<10ms`), transparent configuration, and Arch-style UNIX ergonomics.

## Design Principles

- **KISS & Zero-Bloat:** Pure Go standard library (`os/exec`, `net`, `syscall`, `text/template`, `flag`). No heavy TUI/CLI frameworks or bundled runtimes.
- **User-Space Native:** Orchestrates system binaries (`nginx`, `php-fpm`, `mysqld`/`mariadb`, `mkcert`, `cloudflared`) without root `systemd` services.
- **Silent on Success, Loud on Error:** Clean UNIX output on `stdout`, raw actionable diagnostics on `stderr`.
- **XDG Compliant:**
  - Config: `$XDG_CONFIG_HOME/xabi/` (`~/.config/xabi/`)
  - Data: `$XDG_DATA_HOME/xabi/` (`~/.local/share/xabi/`)
  - Runtime: `$XDG_RUNTIME_DIR/xabi/` (fallback `/tmp/xabi-<uid>/`)

## Build

```sh
# Build native static binary
make build

# Cross-compile for Linux amd64
make build-linux

# Run tests and static analysis
make test
make vet
```

## Install

```sh
make install
```

Installs `xabi` to `~/.local/bin/xabi` by default (`PREFIX=/usr/local` for system-wide install).

## Planned Command Surface

- `xabi init`, `xabi doctor` — User-space initialization and environment checks
- `xabi up`, `xabi down`, `xabi restart`, `xabi status` (`st`) — Process orchestration
- `xabi add`, `xabi rm`, `xabi ls`, `xabi open` — Virtual hosts & reverse proxies
- `xabi tls` — Local HTTPS certificates via `mkcert`
- `xabi php` — Instant PHP version switching & `.php-version` routing
- `xabi conf`, `xabi log`, `xabi share`, `xabi db`, `xabi completion` — Utilities & shell completion

## License

[GPL-3.0](LICENSE)

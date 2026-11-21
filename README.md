# xabi

Minimalist, zero-overhead local web development orchestrator running in user-space on Linux and Windows.

Built in Go using only the standard library for instant startup (`<10ms`), transparent configuration, and Arch-style UNIX ergonomics.

## Design Principles

- **KISS & Zero-Bloat:** Pure Go standard library (`os/exec`, `net`, `syscall`, `text/template`, `flag`). No heavy TUI/CLI frameworks or bundled runtimes.
- **Native User-Space Orchestration:** Manages system binaries (`nginx`/`nginx.exe`, `php-fpm`/`php-cgi.exe`, `mariadbd`/`mysqld`, `mkcert`, `cloudflared`) without root `systemd` services or Windows service wrappers:
  - **Linux:** Unix domain sockets (`php.sock`, `mysql.sock`), `/proc/<pid>` inspection & RSS accounting, `SIGTERM`/`SIGKILL` stop, `SIGHUP`/`SIGUSR2` reload, and `cap_net_bind_service` support.
  - **Windows:** Background daemons via `HideWindow` + `CREATE_NEW_PROCESS_GROUP` (no console popups), `php-cgi.exe -b 127.0.0.1:9000`, `kernel32.dll`/`psapi.dll` PID & RSS tracking, and `nginx.exe -s reload`.
- **Silent on Success, Loud on Error:** Minimal UNIX status lines on `stdout`; raw actionable diagnostics on `stderr`.
- **XDG Compliant:**
  - Config: `$XDG_CONFIG_HOME/xabi/` (`~/.config/xabi/`) — `config.toml`, `sites.toml`, `nginx/`, `certs/`, `php/`
  - Data: `$XDG_DATA_HOME/xabi/` (`~/.local/share/xabi/`) — `db/`, `bin/`
  - Runtime: `$XDG_RUNTIME_DIR/xabi/` (Linux fallback `/tmp/xabi-<uid>/`, Windows `%TEMP%\xabi\`) — `.pid`, `.sock`, logs, Nginx temp buffers

## Build

```sh
# Build native static binary (Linux/macOS)
make build

# Cross-compile for Linux amd64
make build-linux

# Build directly on Windows
go build -trimpath -ldflags="-s -w" -o bin/xabi.exe ./cmd/xabi

# Run tests and static analysis
make test
make vet
```

## Install

```sh
make install
```

Installs `xabi` to `~/.local/bin/xabi` by default (`PREFIX=/usr/local` for system-wide install). On Windows, place `xabi.exe` in a directory on `PATH` (or `%USERPROFILE%\.local\share\xabi\bin`).

## Commands

### Initialization & Diagnostics
- `xabi init [--dry-run] [--force]` — Create XDG layout, default `config.toml`, user-space `nginx.conf`, initialize MariaDB datadir, apply `setcap` (Linux), and run `mkcert -install`.
- `xabi doctor` — Check required/optional binaries, probe ports (`80`, `443`, `3306`), and validate hosts file & XDG permissions.

### Process Orchestrator
- `xabi up [service...]` — Start `nginx`, `php-fpm` (`php-cgi.exe` on Windows), and `mariadb` idempotently.
- `xabi down [service...]` — Stop services in reverse order and clean `.pid` / `.sock` runtime files.
- `xabi restart [--reload] [service...]` — Full restart, or graceful reload (`SIGHUP`/`SIGUSR2` on Linux, `nginx -s reload` on Windows).
- `xabi status [service...]` *(alias: `st`)* — Display real-time `RUNNING`/`STOPPED` state, PID, endpoint, and RSS memory with automatic orphan PID recovery.

### Virtual Hosts, Reverse Proxy & TLS
- `xabi add [name] [--proxy <port|host:port>] [--dir <path>] [--php <ver>]` — Auto-detect project root (Laravel `/public`, PHP, static HTML, or reverse proxy), write Nginx `.conf`, inject `127.0.0.1 <domain>` into hosts (`# BEGIN xabi` block), and reload Nginx if running.
- `xabi rm <name>` — Remove site `.conf`, certificates, `sites.toml` entry, and hosts line.
- `xabi ls` — Print registered sites (`DOMAIN`, `TARGET`, `TLS`, `PHP`).
- `xabi open [name]` — Open site URL (`http://` or `https://`) in default browser (auto-detects current directory when `[name]` is omitted).
- `xabi tls [name] [--off|--disable]` — Issue local trusted certificates via `mkcert`, enable HTTPS + HTTP-to-HTTPS `301` redirect in Nginx, and reload web server.

### Planned Utilities
- `xabi php` — Instant PHP version switching & `.php-version` routing
- `xabi conf`, `xabi log`, `xabi share`, `xabi db`, `xabi completion` — Config editing, raw log tailing, tunneling, DB shell, and shell completion

## License

[GPL-3.0](LICENSE)

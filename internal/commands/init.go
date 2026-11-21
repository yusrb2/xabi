// Package commands implements xabi CLI subcommands.
package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/xdg"
)

// Deps bundles dependencies injected into CLI command handlers.
type Deps struct {
	ResolveXDG func() (xdg.Paths, error)
	Sys        sysutil.System
}

var phpFPMBinCandidates = []string{
	"php-fpm",
	"php-fpm8.4", "php-fpm8.3", "php-fpm8.2", "php-fpm8.1",
	"php-fpm84", "php-fpm83", "php-fpm82", "php-fpm81",
}

var dbDaemonCandidates = []string{"mariadbd", "mysqld"}
var dbInitCandidates = []string{"mariadb-install-db", "mysql_install_db"}

// NewInitCommand returns the `xabi init` command definition.
func NewInitCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "init",
		Summary: "Initialize user-space directories, templates, and local stack",
		Usage:   "xabi init [--dry-run] [--force]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runInit(ctx, args, s, deps)
		},
	}
}

func runInit(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("init", s)
	dryRun := fs.Bool("dry-run", false, "Print planned actions without making changes")
	force := fs.Bool("force", false, "Overwrite existing config.toml and nginx.conf")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		cli.Error(s.Err, "unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}

	paths, err := deps.ResolveXDG()
	if err != nil {
		cli.Error(s.Err, "resolve XDG paths: %v", err)
		return cli.ExitErr
	}

	sys := deps.Sys
	if sys == nil {
		sys = sysutil.OS{}
	}

	if *dryRun {
		cli.Step(s.Out, "dry-run mode: no files or system state will be modified")
	}

	if err := initDirectories(paths, *dryRun, s); err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	cfg, err := initConfigAndTemplates(paths, *dryRun, *force, s)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if err := initCapabilities(ctx, sys, cfg, *dryRun, s); err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if err := initDatabase(ctx, sys, paths, *dryRun, s); err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	initMKCert(ctx, sys, *dryRun, s)
	reportPHPFPM(sys, s)

	cli.Info(s.Out, "xabi initialized (run 'xabi doctor' to verify)")
	return cli.ExitOK
}

func initDirectories(paths xdg.Paths, dryRun bool, s cli.Streams) error {
	dirs := []string{
		paths.ConfigDir,
		paths.NginxDir(),
		paths.SitesDir(),
		paths.CertsDir(),
		paths.PHPDir(),
		paths.DataDir,
		paths.DBDir(),
		paths.BinDir(),
		paths.RuntimeDir,
		paths.NginxTempDir(),
		filepath.Join(paths.NginxTempDir(), "client_body"),
		filepath.Join(paths.NginxTempDir(), "proxy"),
		filepath.Join(paths.NginxTempDir(), "fastcgi"),
		filepath.Join(paths.NginxTempDir(), "uwsgi"),
		filepath.Join(paths.NginxTempDir(), "scgi"),
	}

	if dryRun {
		cli.Info(s.Out, "would ensure XDG directories under %s, %s, %s", paths.ConfigDir, paths.DataDir, paths.RuntimeDir)
		return nil
	}

	if err := paths.Ensure(); err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	cli.Info(s.Out, "ensured XDG directories")
	return nil
}

func initConfigAndTemplates(paths xdg.Paths, dryRun, force bool, s cli.Streams) (config.Config, error) {
	cfgPath := paths.ConfigFile()
	cfg, exists, err := config.LoadOrDefault(cfgPath)
	if err != nil {
		return config.Config{}, fmt.Errorf("load %s: %w", cfgPath, err)
	}

	if !exists || force {
		if force {
			cfg = config.Default()
		}
		if dryRun {
			cli.Info(s.Out, "would write %s", cfgPath)
		} else {
			if err := config.Save(cfgPath, cfg); err != nil {
				return config.Config{}, fmt.Errorf("write %s: %w", cfgPath, err)
			}
			cli.Info(s.Out, "wrote %s", cfgPath)
		}
	} else {
		cli.Info(s.Out, "kept existing %s", cfgPath)
	}

	nginxConfPath := paths.NginxConf()
	if !fileExists(nginxConfPath) || force {
		if dryRun {
			cli.Info(s.Out, "would write %s", nginxConfPath)
		} else {
			rendered, err := config.RenderNginxConf(config.NewNginxTemplateData(paths, cfg.Ports.HTTP))
			if err != nil {
				return config.Config{}, fmt.Errorf("render nginx.conf: %w", err)
			}
			if err := sysutil.WriteFileAtomic(nginxConfPath, []byte(rendered), 0o644); err != nil {
				return config.Config{}, fmt.Errorf("write %s: %w", nginxConfPath, err)
			}
			cli.Info(s.Out, "wrote %s", nginxConfPath)
		}
	} else {
		cli.Info(s.Out, "kept existing %s", nginxConfPath)
	}

	mimePath := filepath.Join(paths.NginxDir(), "mime.types")
	if !fileExists(mimePath) || force {
		if dryRun {
			cli.Info(s.Out, "would write %s", mimePath)
		} else if err := sysutil.WriteFileAtomic(mimePath, []byte(config.DefaultMimeTypes()), 0o644); err != nil {
			return config.Config{}, fmt.Errorf("write %s: %w", mimePath, err)
		}
	}

	return cfg, nil
}

func initCapabilities(ctx context.Context, sys sysutil.System, cfg config.Config, dryRun bool, s cli.Streams) error {
	binPath, ok := sys.LookBinary(cfg.WebServer)
	if !ok {
		cli.Warn(s.Err, "%s not found in PATH; install it before starting web services", cfg.WebServer)
		return nil
	}
	resolved, err := sys.EvalSymlinks(binPath)
	if err != nil {
		resolved = binPath
	}

	if cfg.Ports.HTTP >= 1024 && cfg.Ports.HTTPS >= 1024 {
		cli.Info(s.Out, "%s (%s): unprivileged ports (%d/%d), cap_net_bind_service not required",
			cfg.WebServer, resolved, cfg.Ports.HTTP, cfg.Ports.HTTPS)
		return nil
	}

	capSt := sys.CheckNetBindCap(resolved)
	if !capSt.Supported {
		return nil
	}
	if capSt.Set {
		cli.Info(s.Out, "%s (%s): cap_net_bind_service already enabled", cfg.WebServer, resolved)
		return nil
	}

	if dryRun {
		cli.Info(s.Out, "would grant cap_net_bind_service=+ep on %s", resolved)
		return nil
	}

	if err := sys.SetNetBindCap(ctx, resolved); err != nil {
		if sysutil.IsPermissionErr(err) {
			cli.Warn(s.Err, "cannot set cap_net_bind_service on %s without privileges; run:\n  sudo setcap 'cap_net_bind_service=+ep' %s", resolved, resolved)
			return nil
		}
		cli.Warn(s.Err, "setcap failed on %s: %v", resolved, err)
		return nil
	}

	cli.Info(s.Out, "enabled cap_net_bind_service on %s", resolved)
	return nil
}

func initDatabase(ctx context.Context, sys sysutil.System, paths xdg.Paths, dryRun bool, s cli.Streams) error {
	dbDir := paths.DBDir()
	if fileExists(filepath.Join(dbDir, "mysql")) {
		cli.Info(s.Out, "kept existing database in %s", dbDir)
		return nil
	}

	if initBin, ok := sys.LookBinary(dbInitCandidates...); ok {
		if dryRun {
			cli.Info(s.Out, "would run %s --datadir=%s", initBin, dbDir)
			return nil
		}
		out, err := sys.Run(ctx, initBin, "--datadir="+dbDir)
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return fmt.Errorf("initialize user-space database (%s): %s: %w", initBin, msg, err)
			}
			return fmt.Errorf("initialize user-space database (%s): %w", initBin, err)
		}
		cli.Info(s.Out, "initialized user-space database in %s", dbDir)
		return nil
	}

	if mysqldBin, ok := sys.LookBinary("mysqld"); ok {
		if dryRun {
			cli.Info(s.Out, "would run %s --initialize-insecure --datadir=%s", mysqldBin, dbDir)
			return nil
		}
		out, err := sys.Run(ctx, mysqldBin, "--initialize-insecure", "--datadir="+dbDir)
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return fmt.Errorf("initialize user-space database (%s): %s: %w", mysqldBin, msg, err)
			}
			return fmt.Errorf("initialize user-space database (%s): %w", mysqldBin, err)
		}
		cli.Info(s.Out, "initialized user-space database in %s", dbDir)
		return nil
	}

	cli.Warn(s.Err, "mariadb-install-db/mysqld not found; skipping user-space database initialization")
	return nil
}

func initMKCert(ctx context.Context, sys sysutil.System, dryRun bool, s cli.Streams) {
	mkcertBin, ok := sys.LookBinary("mkcert")
	if !ok {
		cli.Warn(s.Err, "mkcert not found; local HTTPS certificates will be unavailable until installed")
		return
	}
	if dryRun {
		cli.Info(s.Out, "would run %s -install", mkcertBin)
		return
	}
	out, err := sys.Run(ctx, mkcertBin, "-install")
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			cli.Warn(s.Err, "mkcert -install failed (%s): %v", msg, err)
		} else {
			cli.Warn(s.Err, "mkcert -install failed: %v", err)
		}
		return
	}
	cli.Info(s.Out, "installed local mkcert root CA")
}

func reportPHPFPM(sys sysutil.System, s cli.Streams) {
	if phpBin, ok := sys.LookBinary(phpFPMBinCandidates...); ok {
		cli.Info(s.Out, "detected php-fpm at %s", phpBin)
		return
	}
	cli.Warn(s.Err, "php-fpm not found in PATH")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

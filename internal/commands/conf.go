package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/php"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

// NewConfCommand returns the `xabi conf` command definition.
func NewConfCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "conf",
		Summary: "Open xabi, nginx, php, or site configuration in $EDITOR",
		Usage:   "xabi conf [config|nginx|php|sites|<site>] [--path]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runConf(ctx, args, s, deps)
		},
	}
}

func runConf(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("conf", s)
	printPath := fs.Bool("path", false, "Print the resolved configuration file path and exit")
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 1 {
		cli.Error(s.Err, "expected at most one target")
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	target := "config"
	if fs.NArg() == 1 {
		target = strings.TrimSpace(fs.Arg(0))
	}

	resolved, err := resolveConfPath(ctx, target, paths, cfg, sys, deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if *printPath {
		fmt.Fprintln(s.Out, resolved)
		return cli.ExitOK
	}

	edBin, edArgs, err := resolveEditor(sys)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}
	edArgs = append(edArgs, resolved)

	if err := sys.RunAttached(ctx, edBin, edArgs, s.In, s.Out, s.Err); err != nil {
		cli.Error(s.Err, "editor exited with error: %v", err)
		return cli.ExitErr
	}
	return cli.ExitOK
}

func resolveConfPath(
	ctx context.Context,
	target string,
	paths xdg.Paths,
	cfg config.Config,
	sys sysutil.System,
	deps Deps,
) (string, error) {
	switch strings.ToLower(target) {
	case "", "config", "xabi":
		p := paths.ConfigFile()
		if !fileExists(p) {
			return "", fmt.Errorf("%s not found (run 'xabi init' first)", p)
		}
		return p, nil
	case "nginx":
		p := paths.NginxConf()
		if !fileExists(p) {
			return "", fmt.Errorf("%s not found (run 'xabi init' first)", p)
		}
		return p, nil
	case "sites":
		p := paths.SitesFile()
		if !fileExists(p) {
			return "", fmt.Errorf("%s not found (no sites registered)", p)
		}
		return p, nil
	case "php":
		return resolvePHPConfPath(ctx, paths, cfg, sys, deps)
	default:
		_, domain, err := vhost.NormalizeSiteName(target, cfg.TLD)
		if err != nil {
			return "", fmt.Errorf("unknown config target %q", target)
		}
		confPath := vhost.SiteConfPath(paths, domain)
		if fileExists(confPath) {
			return confPath, nil
		}
		reg, regErr := vhost.LoadRegistry(paths.SitesFile())
		if regErr == nil {
			if site, ok := reg.FindByDomain(domain); ok {
				p := vhost.SiteConfPath(paths, site.Domain)
				if fileExists(p) {
					return p, nil
				}
			}
		}
		return "", fmt.Errorf("unknown config target or site %q", target)
	}
}

func resolvePHPConfPath(
	ctx context.Context,
	paths xdg.Paths,
	cfg config.Config,
	sys sysutil.System,
	deps Deps,
) (string, error) {
	if runtime.GOOS != "windows" {
		fpmConf := filepath.Join(paths.PHPDir(), "php-fpm.conf")
		if fileExists(fpmConf) {
			return fpmConf, nil
		}
	}

	ver := cfg.PHPDefault
	if ver == "" || ver == "system" {
		if resolvedVer, _, ok := daemon.ResolvePHPWithDiscover(ctx, paths, sys, ver, discoverPHPCallback(deps)); ok {
			ver = resolvedVer
		}
	}

	installed := discoverPHPVersions(ctx, paths, sys, deps)
	if inst, ok := php.FindVersion(installed, ver); ok {
		for _, bin := range []string{inst.CLI, inst.FPM} {
			if bin == "" {
				continue
			}
			iniPath := filepath.Join(filepath.Dir(bin), "php.ini")
			if fileExists(iniPath) {
				return iniPath, nil
			}
		}
		if inst.CLI != "" {
			if out, err := sys.Run(ctx, inst.CLI, "-r", "echo php_ini_loaded_file();"); err == nil {
				loaded := strings.TrimSpace(string(out))
				if loaded != "" && fileExists(loaded) {
					return loaded, nil
				}
			}
		}
	}

	if phpCLI, ok := sys.LookBinary("php"); ok {
		iniNear := filepath.Join(filepath.Dir(phpCLI), "php.ini")
		if fileExists(iniNear) {
			return iniNear, nil
		}
		if out, err := sys.Run(ctx, phpCLI, "-r", "echo php_ini_loaded_file();"); err == nil {
			loaded := strings.TrimSpace(string(out))
			if loaded != "" && fileExists(loaded) {
				return loaded, nil
			}
		}
	}

	fpmConf := filepath.Join(paths.PHPDir(), "php-fpm.conf")
	if fileExists(fpmConf) {
		return fpmConf, nil
	}
	return "", fmt.Errorf("no PHP config file (php-fpm.conf or php.ini) found for active PHP %q", ver)
}

func resolveEditor(sys sysutil.System) (string, []string, error) {
	for _, envKey := range []string{"VISUAL", "EDITOR"} {
		raw := strings.TrimSpace(os.Getenv(envKey))
		if raw == "" {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) > 0 {
			return fields[0], fields[1:], nil
		}
	}

	if runtime.GOOS == "windows" {
		if bin, ok := sys.LookBinary("notepad.exe", "notepad"); ok {
			return bin, nil, nil
		}
		return "notepad.exe", nil, nil
	}

	if bin, ok := sys.LookBinary("nvim", "vim", "nano", "vi"); ok {
		return bin, nil, nil
	}
	return "", nil, fmt.Errorf("no editor found; set $EDITOR")
}

func discoverPHPCallback(deps Deps) func(context.Context, xdg.Paths, sysutil.System) []php.Installation {
	if deps.DiscoverPHP != nil {
		return deps.DiscoverPHP
	}
	return php.Discover
}

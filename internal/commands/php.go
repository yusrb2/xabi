package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/php"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

// NewPHPCommand returns the `xabi php` command definition.
func NewPHPCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "php",
		Summary: "List, switch global, or pin per-project PHP versions",
		Usage:   "xabi php [version | use <version> | pin <version>]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runPHP(ctx, args, s, deps)
		},
	}
}

func runPHP(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("php", s)
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	pos := fs.Args()
	switch len(pos) {
	case 0:
		return listPHPVersions(ctx, paths, cfg, sys, deps, s)
	case 1:
		if pos[0] == "pin" || pos[0] == "use" {
			cli.Error(s.Err, "usage: xabi php %s <version>", pos[0])
			return cli.ExitUsage
		}
		return switchGlobalPHP(ctx, pos[0], paths, cfg, sys, deps, s)
	case 2:
		switch pos[0] {
		case "use":
			return switchGlobalPHP(ctx, pos[1], paths, cfg, sys, deps, s)
		case "pin":
			return pinProjectPHP(ctx, pos[1], paths, cfg, sys, deps, s)
		default:
			cli.Error(s.Err, "unknown subcommand %q (expected 'use' or 'pin')", pos[0])
			return cli.ExitUsage
		}
	default:
		cli.Error(s.Err, "too many arguments")
		return cli.ExitUsage
	}
}

func listPHPVersions(ctx context.Context, paths xdg.Paths, cfg config.Config, sys sysutil.System, deps Deps, s cli.Streams) int {
	installed := discoverPHPVersions(ctx, paths, sys, deps)
	if len(installed) == 0 {
		cli.Info(s.Out, "no PHP installations detected")
		return cli.ExitOK
	}

	activeVer := cfg.PHPDefault
	if activeVer == "" || activeVer == "system" {
		if ver, _, ok := daemon.ResolvePHPVersionIn(ctx, paths, sys, cfg.PHPDefault); ok {
			activeVer = ver
		}
	}

	tw := tabwriter.NewWriter(s.Out, 0, 4, 2, ' ', 0)
	for _, inst := range installed {
		marker := " "
		if inst.Version == activeVer {
			marker = "*"
		}
		binPath := inst.CLI
		if binPath == "" {
			binPath = inst.FPM
		}
		fmt.Fprintf(tw, "%s %s\t%s\t(%s)\n", marker, inst.Version, filepath.ToSlash(binPath), inst.Source)
	}
	_ = tw.Flush()

	if cwd, err := os.Getwd(); err == nil {
		if dotVer, found, dotErr := php.ReadDotPHPVersion(cwd); dotErr == nil && found {
			cli.Info(s.Out, "project .php-version: %s", dotVer)
		}
	}
	return cli.ExitOK
}

func switchGlobalPHP(ctx context.Context, rawVer string, paths xdg.Paths, cfg config.Config, sys sysutil.System, deps Deps, s cli.Streams) int {
	ver, err := php.NormalizeVersion(rawVer)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	installed := discoverPHPVersions(ctx, paths, sys, deps)
	inst, ok := php.FindVersion(installed, ver)
	if !ok {
		cli.Error(s.Err, "%s", missingPHPVersionMsg(ver, installed))
		return cli.ExitErr
	}

	if err := php.ActivateShim(paths.BinDir(), inst); err != nil {
		cli.Error(s.Err, "update PHP shim: %v", err)
		return cli.ExitErr
	}

	cfg.PHPDefault = inst.Version
	if err := config.Save(paths.ConfigFile(), cfg); err != nil {
		cli.Error(s.Err, "save %s: %v", paths.ConfigFile(), err)
		return cli.ExitErr
	}

	if err := regenerateDefaultPHPVHosts(paths, cfg); err != nil {
		cli.Warn(s.Err, "active PHP switched, but failed regenerating vhosts: %v", err)
	}

	if err := refreshRunningPHPAndWeb(ctx, paths, cfg, sys, deps); err != nil {
		cli.Warn(s.Err, "active PHP switched, but service refresh failed: %v", err)
	}

	displayBin := inst.CLI
	if displayBin == "" {
		displayBin = inst.FPM
	}
	cli.Info(s.Out, "active PHP set to %s (%s)", inst.Version, filepath.ToSlash(displayBin))
	return cli.ExitOK
}

func pinProjectPHP(ctx context.Context, rawVer string, paths xdg.Paths, cfg config.Config, sys sysutil.System, deps Deps, s cli.Streams) int {
	ver, err := php.NormalizeVersion(rawVer)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}
	if ver == "system" {
		cli.Error(s.Err, "cannot pin 'system'; specify a concrete version (e.g. 8.3)")
		return cli.ExitErr
	}

	installed := discoverPHPVersions(ctx, paths, sys, deps)
	if _, ok := php.FindVersion(installed, ver); !ok {
		cli.Error(s.Err, "%s", missingPHPVersionMsg(ver, installed))
		return cli.ExitErr
	}

	cwd, err := os.Getwd()
	if err != nil {
		cli.Error(s.Err, "determine working directory: %v", err)
		return cli.ExitErr
	}
	if _, err := php.WriteDotPHPVersion(cwd, ver); err != nil {
		cli.Error(s.Err, "write .php-version: %v", err)
		return cli.ExitErr
	}

	regPath := paths.SitesFile()
	reg, err := vhost.LoadRegistry(regPath)
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}

	site, found := reg.FindByDir(cwd)
	if !found {
		cli.Info(s.Out, "wrote .php-version (%s)", ver)
		return cli.ExitOK
	}

	site.PHP = ver
	if site.Kind == vhost.KindStatic {
		site.Kind = vhost.KindPHP
	}
	if err := os.MkdirAll(paths.SitesDir(), 0o755); err != nil {
		cli.Error(s.Err, "create sites directory: %v", err)
		return cli.ExitErr
	}
	rendered, err := vhost.RenderNginxVHost(vhost.BuildServerTemplateData(site, paths, cfg))
	if err != nil {
		cli.Error(s.Err, "render vhost config: %v", err)
		return cli.ExitErr
	}
	if err := sysutil.WriteFileAtomic(vhost.SiteConfPath(paths, site.Domain), []byte(rendered), 0o644); err != nil {
		cli.Error(s.Err, "write vhost config: %v", err)
		return cli.ExitErr
	}
	reg.Upsert(site)
	if err := vhost.SaveRegistry(regPath, reg); err != nil {
		cli.Error(s.Err, "save sites registry: %v", err)
		return cli.ExitErr
	}

	if err := refreshRunningPHPAndWeb(ctx, paths, cfg, sys, deps); err != nil {
		cli.Warn(s.Err, "pinned PHP %s, but service refresh failed: %v", ver, err)
	}

	cli.Info(s.Out, "pinned PHP %s for %s (.php-version)", ver, site.Domain)
	return cli.ExitOK
}

func regenerateDefaultPHPVHosts(paths xdg.Paths, cfg config.Config) error {
	reg, err := vhost.LoadRegistry(paths.SitesFile())
	if err != nil {
		return err
	}
	for _, site := range reg.Sites {
		if site.Kind != vhost.KindPHP || site.PHP != "" {
			continue
		}
		rendered, err := vhost.RenderNginxVHost(vhost.BuildServerTemplateData(site, paths, cfg))
		if err != nil {
			return err
		}
		if err := sysutil.WriteFileAtomic(vhost.SiteConfPath(paths, site.Domain), []byte(rendered), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func refreshRunningPHPAndWeb(ctx context.Context, paths xdg.Paths, cfg config.Config, sys sysutil.System, deps Deps) error {
	var mgr daemon.Controller
	if deps.NewDaemon != nil {
		mgr = deps.NewDaemon(paths, cfg, sys)
	} else {
		mgr = daemon.NewManager(paths, cfg, sys)
	}

	webSvc := cfg.WebServer
	if webSvc == "" {
		webSvc = daemon.ServiceNginx
	}
	statuses, err := mgr.Status(ctx, []string{daemon.ServicePHPFPM, webSvc})
	if err != nil {
		return err
	}
	for _, st := range statuses {
		if st.State != daemon.StateRunning {
			continue
		}
		reloadOnly := st.Name == webSvc
		if _, err := mgr.Restart(ctx, []string{st.Name}, reloadOnly); err != nil {
			return err
		}
	}
	return nil
}

func discoverPHPVersions(ctx context.Context, paths xdg.Paths, sys sysutil.System, deps Deps) []php.Installation {
	if deps.DiscoverPHP != nil {
		out := append([]php.Installation(nil), deps.DiscoverPHP(ctx, paths, sys)...)
		php.SortInstallations(out)
		return out
	}
	return php.Discover(ctx, paths, sys)
}

func missingPHPVersionMsg(ver string, installed []php.Installation) string {
	if len(installed) == 0 {
		return fmt.Sprintf("PHP %s not found (no PHP installations detected)", ver)
	}
	avail := make([]string, 0, len(installed))
	for _, inst := range installed {
		avail = append(avail, inst.Version)
	}
	return fmt.Sprintf("PHP %s not found (available: %s)", ver, strings.Join(avail, ", "))
}

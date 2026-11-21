package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/daemon"
	"github.com/yusrb2/xabi/internal/hosts"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
	"github.com/yusrb2/xabi/internal/xdg"
)

// NewAddCommand returns the `xabi add` command definition.
func NewAddCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "add",
		Summary: "Register a local domain, generate Nginx vhost, and update hosts",
		Usage:   "xabi add [name] [--proxy <port|host:port>] [--dir <path>] [--php <ver>]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runAdd(ctx, args, s, deps)
		},
	}
}

// NewRmCommand returns the `xabi rm` command definition.
func NewRmCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "rm",
		Summary: "Remove a local site vhost, certificates, and hosts entry",
		Usage:   "xabi rm <name>",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runRm(ctx, args, s, deps)
		},
	}
}

// NewLsCommand returns the `xabi ls` command definition.
func NewLsCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "ls",
		Summary: "List registered local sites",
		Usage:   "xabi ls",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runLs(ctx, args, s, deps)
		},
	}
}

// NewOpenCommand returns the `xabi open` command definition.
func NewOpenCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "open",
		Summary: "Open a site URL in the default browser",
		Usage:   "xabi open [name]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runOpen(ctx, args, s, deps)
		},
	}
}

func runAdd(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("add", s)
	proxyFlag := fs.String("proxy", "", "Reverse proxy target port or host:port")
	dirFlag := fs.String("dir", "", "Project directory (defaults to current working directory)")
	phpFlag := fs.String("php", "", "Specific PHP version for this site (e.g. 8.3)")
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 1 {
		cli.Error(s.Err, "expected at most one site name")
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	projectDir := strings.TrimSpace(*dirFlag)
	if projectDir == "" {
		projectDir, err = os.Getwd()
		if err != nil {
			cli.Error(s.Err, "determine working directory: %v", err)
			return cli.ExitErr
		}
	}

	rawName := vhost.SanitizeDirName(projectDir)
	if fs.NArg() == 1 {
		rawName = fs.Arg(0)
	}
	name, domain, err := vhost.NormalizeSiteName(rawName, cfg.TLD)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	phpVer := strings.TrimSpace(*phpFlag)
	if phpVer == "" {
		phpVer = readDotPHPVersion(projectDir)
	}

	kind, root, proxyURL, err := vhost.DetectProject(projectDir, *proxyFlag, phpVer)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if err := os.MkdirAll(paths.SitesDir(), 0o755); err != nil {
		cli.Error(s.Err, "create sites directory: %v", err)
		return cli.ExitErr
	}

	regPath := paths.SitesFile()
	reg, err := vhost.LoadRegistry(regPath)
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}

	prevSite, hadPrev := reg.FindByDomain(domain)
	site := vhost.Site{
		Name:   name,
		Domain: domain,
		Kind:   kind,
		Root:   root,
		Proxy:  proxyURL,
		PHP:    phpVer,
		TLS:    hadPrev && prevSite.TLS,
	}

	confPath := vhost.SiteConfPath(paths, domain)
	prevConf, confErr := os.ReadFile(confPath)
	rendered, err := vhost.RenderNginxVHost(vhost.BuildServerTemplateData(site, paths, cfg))
	if err != nil {
		cli.Error(s.Err, "render vhost config: %v", err)
		return cli.ExitErr
	}
	if err := sysutil.WriteFileAtomic(confPath, []byte(rendered), 0o644); err != nil {
		cli.Error(s.Err, "write %s: %v", confPath, err)
		return cli.ExitErr
	}

	reg.Upsert(site)
	if err := vhost.SaveRegistry(regPath, reg); err != nil {
		restoreSiteFiles(confPath, prevConf, confErr == nil)
		cli.Error(s.Err, "save sites registry: %v", err)
		return cli.ExitErr
	}

	hostsMgr := hosts.NewManager(sys.HostsPath())
	if _, err := hostsMgr.Add(domain); err != nil {
		// Keep registry/vhost consistent if hosts update failed on first registration.
		if !hadPrev {
			reg.Remove(domain)
			_ = vhost.SaveRegistry(regPath, reg)
		} else {
			reg.Upsert(prevSite)
			_ = vhost.SaveRegistry(regPath, reg)
		}
		restoreSiteFiles(confPath, prevConf, confErr == nil)
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if err := reloadWebServerIfRunning(ctx, paths, cfg, sys, deps); err != nil {
		cli.Warn(s.Err, "site added, but web server reload failed: %v", err)
	}

	target := filepath.ToSlash(site.Root)
	if site.Kind == vhost.KindProxy {
		target = site.Proxy
	}
	cli.Info(s.Out, "linked %s -> %s", siteURL(site, cfg), target)
	return cli.ExitOK
}

func runRm(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("rm", s)
	if err := cli.ParseFlags(fs, args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		cli.Error(s.Err, "usage: xabi rm <name>")
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	_, domain, err := vhost.NormalizeSiteName(fs.Arg(0), cfg.TLD)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	regPath := paths.SitesFile()
	reg, err := vhost.LoadRegistry(regPath)
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}
	_, regRemoved := reg.Remove(domain)
	if regRemoved {
		if err := vhost.SaveRegistry(regPath, reg); err != nil {
			cli.Error(s.Err, "save sites registry: %v", err)
			return cli.ExitErr
		}
	}

	confPath := vhost.SiteConfPath(paths, domain)
	confRemoved := false
	if err := os.Remove(confPath); err == nil {
		confRemoved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		cli.Error(s.Err, "remove %s: %v", confPath, err)
		return cli.ExitErr
	}

	removeSiteCerts(paths, domain)

	hostsRemoved, err := hosts.NewManager(sys.HostsPath()).Remove(domain)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if !regRemoved && !confRemoved && !hostsRemoved {
		cli.Error(s.Err, "site %q is not registered", domain)
		return cli.ExitErr
	}

	if err := reloadWebServerIfRunning(ctx, paths, cfg, sys, deps); err != nil {
		cli.Warn(s.Err, "site removed, but web server reload failed: %v", err)
	}

	cli.Info(s.Out, "unlinked %s", domain)
	return cli.ExitOK
}

func runLs(_ context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("ls", s)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		cli.Error(s.Err, "unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}

	paths, _, _, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	reg, err := vhost.LoadRegistry(paths.SitesFile())
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}
	if len(reg.Sites) == 0 {
		cli.Info(s.Out, "no sites registered")
		return cli.ExitOK
	}

	tw := tabwriter.NewWriter(s.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DOMAIN\tTARGET\tTLS\tPHP")
	for _, site := range reg.Sites {
		target := fmt.Sprintf("%s -> %s", site.Kind, filepath.ToSlash(site.Root))
		if site.Kind == vhost.KindProxy {
			target = "proxy -> " + site.Proxy
		}
		tlsCol := "off"
		if site.TLS {
			tlsCol = "on"
		}
		phpCol := "-"
		if site.Kind == vhost.KindPHP {
			phpCol = site.PHP
			if phpCol == "" {
				phpCol = "default"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", site.Domain, target, tlsCol, phpCol)
	}
	_ = tw.Flush()
	return cli.ExitOK
}

func runOpen(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("open", s)
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 1 {
		cli.Error(s.Err, "expected at most one site name")
		return cli.ExitUsage
	}

	paths, cfg, sys, err := loadSiteContext(deps)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	reg, err := vhost.LoadRegistry(paths.SitesFile())
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}

	var site vhost.Site
	var found bool
	if fs.NArg() == 1 {
		_, domain, err := vhost.NormalizeSiteName(fs.Arg(0), cfg.TLD)
		if err != nil {
			cli.Error(s.Err, "%v", err)
			return cli.ExitErr
		}
		site, found = reg.FindByDomain(domain)
		if !found {
			cli.Error(s.Err, "site %q is not registered", domain)
			return cli.ExitErr
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			cli.Error(s.Err, "determine working directory: %v", err)
			return cli.ExitErr
		}
		site, found = reg.FindByDir(cwd)
		if !found {
			cli.Error(s.Err, "no site registered for directory %s", cwd)
			return cli.ExitErr
		}
	}

	targetURL := siteURL(site, cfg)
	if err := sys.OpenURL(ctx, targetURL); err != nil {
		cli.Error(s.Err, "open %s: %v", targetURL, err)
		return cli.ExitErr
	}
	cli.Info(s.Out, "opened %s", targetURL)
	return cli.ExitOK
}

func loadSiteContext(deps Deps) (xdg.Paths, config.Config, sysutil.System, error) {
	paths, err := deps.ResolveXDG()
	if err != nil {
		return xdg.Paths{}, config.Config{}, nil, fmt.Errorf("resolve XDG paths: %w", err)
	}
	cfg, _, err := config.LoadOrDefault(paths.ConfigFile())
	if err != nil {
		return xdg.Paths{}, config.Config{}, nil, fmt.Errorf("load %s: %w", paths.ConfigFile(), err)
	}
	sys := deps.Sys
	if sys == nil {
		sys = sysutil.OS{BinDir: paths.BinDir()}
	}
	return paths, cfg, sys, nil
}

func reloadWebServerIfRunning(ctx context.Context, paths xdg.Paths, cfg config.Config, sys sysutil.System, deps Deps) error {
	var mgr daemon.Controller
	if deps.NewDaemon != nil {
		mgr = deps.NewDaemon(paths, cfg, sys)
	} else {
		mgr = daemon.NewManager(paths, cfg, sys)
	}

	svc := cfg.WebServer
	if svc == "" {
		svc = daemon.ServiceNginx
	}
	statuses, err := mgr.Status(ctx, []string{svc})
	if err != nil {
		return err
	}
	if len(statuses) > 0 && statuses[0].State == daemon.StateRunning {
		_, err := mgr.Restart(ctx, []string{svc}, true)
		return err
	}
	return nil
}

func siteURL(site vhost.Site, cfg config.Config) string {
	if site.TLS {
		if cfg.Ports.HTTPS != 443 {
			return "https://" + site.Domain + ":" + strconv.Itoa(cfg.Ports.HTTPS)
		}
		return "https://" + site.Domain
	}
	if cfg.Ports.HTTP != 80 {
		return "http://" + site.Domain + ":" + strconv.Itoa(cfg.Ports.HTTP)
	}
	return "http://" + site.Domain
}

func removeSiteCerts(paths xdg.Paths, domain string) {
	for _, suffix := range []string{".pem", "-key.pem", ".crt", ".key"} {
		_ = os.Remove(filepath.Join(paths.CertsDir(), domain+suffix))
	}
}

func restoreSiteFiles(confPath string, prev []byte, hadPrev bool) {
	if hadPrev {
		_ = sysutil.WriteFileAtomic(confPath, prev, 0o644)
		return
	}
	_ = os.Remove(confPath)
}

func readDotPHPVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".php-version"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.Split(string(data), "\n")[0])
	return strings.TrimPrefix(line, "v")
}

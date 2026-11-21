package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yusrb2/xabi/internal/cli"
	"github.com/yusrb2/xabi/internal/sysutil"
	"github.com/yusrb2/xabi/internal/vhost"
)

// NewTLSCommand returns the `xabi tls` command definition.
func NewTLSCommand(deps Deps) cli.Command {
	return cli.Command{
		Name:    "tls",
		Summary: "Enable or disable local HTTPS via mkcert",
		Usage:   "xabi tls [name] [--off|--disable]",
		Run: func(ctx context.Context, args []string, s cli.Streams) int {
			return runTLS(ctx, args, s, deps)
		},
	}
}

func runTLS(ctx context.Context, args []string, s cli.Streams, deps Deps) int {
	fs := cli.NewFlagSet("tls", s)
	off := fs.Bool("off", false, "Disable TLS for the site and remove its certificates")
	disable := fs.Bool("disable", false, "Alias for --off")
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

	regPath := paths.SitesFile()
	reg, err := vhost.LoadRegistry(regPath)
	if err != nil {
		cli.Error(s.Err, "load sites registry: %v", err)
		return cli.ExitErr
	}

	site, err := resolveRegisteredSite(fs.Args(), reg, cfg.TLD)
	if err != nil {
		cli.Error(s.Err, "%v", err)
		return cli.ExitErr
	}

	if *off || *disable {
		removeSiteCerts(paths, site.Domain, site.CertFile, site.KeyFile)
		site.TLS = false
		site.CertFile = ""
		site.KeyFile = ""
	} else {
		mkcertBin, ok := sys.LookBinary("mkcert")
		if !ok {
			cli.Error(s.Err, "%s", missingMKCertMessage())
			return cli.ExitErr
		}
		if err := os.MkdirAll(paths.CertsDir(), 0o755); err != nil {
			cli.Error(s.Err, "create certs directory: %v", err)
			return cli.ExitErr
		}

		certPath := filepath.Join(paths.CertsDir(), site.Domain+".pem")
		keyPath := filepath.Join(paths.CertsDir(), site.Domain+"-key.pem")
		out, err := sys.Run(ctx, mkcertBin, "-cert-file", certPath, "-key-file", keyPath, site.Domain, "*."+site.Domain)
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				cli.Error(s.Err, "mkcert failed: %s: %v", msg, err)
			} else {
				cli.Error(s.Err, "mkcert failed: %v", err)
			}
			return cli.ExitErr
		}
		if !fileExists(certPath) || !fileExists(keyPath) {
			cli.Error(s.Err, "mkcert did not produce expected certificate files at %s", paths.CertsDir())
			return cli.ExitErr
		}

		site.TLS = true
		site.CertFile = certPath
		site.KeyFile = keyPath
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
	confPath := vhost.SiteConfPath(paths, site.Domain)
	if err := sysutil.WriteFileAtomic(confPath, []byte(rendered), 0o644); err != nil {
		cli.Error(s.Err, "write %s: %v", confPath, err)
		return cli.ExitErr
	}

	reg.Upsert(site)
	if err := vhost.SaveRegistry(regPath, reg); err != nil {
		cli.Error(s.Err, "save sites registry: %v", err)
		return cli.ExitErr
	}

	if err := reloadWebServerIfRunning(ctx, paths, cfg, sys, deps); err != nil {
		cli.Warn(s.Err, "TLS updated, but web server reload failed: %v", err)
	}

	if site.TLS {
		cli.Info(s.Out, "enabled TLS for %s", siteURL(site, cfg))
	} else {
		cli.Info(s.Out, "disabled TLS for %s", siteURL(site, cfg))
	}
	return cli.ExitOK
}

func missingMKCertMessage() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("mkcert.exe not found in PATH; install via 'choco install mkcert' or 'winget install FiloSottile.mkcert'")
	}
	return "mkcert not found in PATH; install via your package manager (e.g. 'sudo pacman -S mkcert' or 'sudo apt install mkcert')"
}

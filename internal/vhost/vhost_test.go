package vhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/xdg"
)

func TestDetectProjectLaravelPHPStaticAndProxy(t *testing.T) {
	// Laravel / Symfony /public/index.php
	laravelDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(laravelDir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laravelDir, "public", "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, root, _, err := DetectProject(laravelDir, "", "")
	if err != nil {
		t.Fatalf("DetectProject(laravel) failed: %v", err)
	}
	if kind != KindPHP || root != filepath.Join(laravelDir, "public") {
		t.Errorf("got (%s, %q), want (php, .../public)", kind, root)
	}

	// Standard PHP
	phpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(phpDir, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, root, _, err = DetectProject(phpDir, "", "")
	if err != nil {
		t.Fatalf("DetectProject(php) failed: %v", err)
	}
	if kind != KindPHP || root != phpDir {
		t.Errorf("got (%s, %q), want (php, %q)", kind, root, phpDir)
	}

	// Static HTML
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, root, _, err = DetectProject(staticDir, "", "")
	if err != nil {
		t.Fatalf("DetectProject(static) failed: %v", err)
	}
	if kind != KindStatic || root != staticDir {
		t.Errorf("got (%s, %q), want (static, %q)", kind, root, staticDir)
	}

	// Proxy mode
	kind, root, proxyURL, err := DetectProject(staticDir, "3000", "")
	if err != nil {
		t.Fatalf("DetectProject(proxy) failed: %v", err)
	}
	if kind != KindProxy || proxyURL != "http://127.0.0.1:3000" || root != staticDir {
		t.Errorf("got (%s, %q, %q)", kind, root, proxyURL)
	}
}

func TestNormalizeSiteNameAndProxy(t *testing.T) {
	name, domain, err := NormalizeSiteName("MyApp.test", "test")
	if err != nil || name != "myapp" || domain != "myapp.test" {
		t.Fatalf("NormalizeSiteName = (%q, %q, %v)", name, domain, err)
	}

	for _, tc := range []struct {
		in   string
		want string
	}{
		{"8080", "http://127.0.0.1:8080"},
		{":4000", "http://127.0.0.1:4000"},
		{"localhost:5173", "http://localhost:5173"},
		{"https://127.0.0.1:8443/", "https://127.0.0.1:8443"},
	} {
		got, err := NormalizeProxy(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeProxy(%q) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestRenderNginxVHostForwardSlashAndProxyWebSocket(t *testing.T) {
	paths := xdg.Paths{
		ConfigDir:  `C:\Users\dev\.config\xabi`,
		DataDir:    `C:\Users\dev\.local\share\xabi`,
		RuntimeDir: `C:\Users\dev\AppData\Local\Temp\xabi`,
	}
	cfg := config.Default()

	phpSite := Site{
		Name:   "laravel",
		Domain: "laravel.test",
		Kind:   KindPHP,
		Root:   `C:\Users\dev\code\laravel\public`,
	}
	out, err := RenderNginxVHost(BuildServerTemplateDataForOS(phpSite, paths, cfg, "windows"))
	if err != nil {
		t.Fatalf("RenderNginxVHost(php) failed: %v", err)
	}
	if strings.Contains(out, `\`) && !strings.Contains(out, `\.`) {
		t.Errorf("found raw backslash in Windows nginx vhost:\n%s", out)
	}
	if !strings.Contains(out, `root "C:/Users/dev/code/laravel/public";`) {
		t.Errorf("expected forward-slash root in nginx vhost:\n%s", out)
	}
	if !strings.Contains(out, "try_files $uri $uri/ /index.php?$query_string;") {
		t.Errorf("expected PHP try_files in nginx vhost:\n%s", out)
	}

	proxySite := Site{
		Name:   "api",
		Domain: "api.test",
		Kind:   KindProxy,
		Proxy:  "http://127.0.0.1:3000",
	}
	pOut, err := RenderNginxVHost(BuildServerTemplateDataForOS(proxySite, paths, cfg, "linux"))
	if err != nil {
		t.Fatalf("RenderNginxVHost(proxy) failed: %v", err)
	}
	for _, want := range []string{
		"proxy_pass http://127.0.0.1:3000;",
		"proxy_http_version 1.1;",
		"proxy_set_header Upgrade $http_upgrade;",
		`proxy_set_header Connection "upgrade";`,
		"proxy_set_header X-Forwarded-Proto $scheme;",
	} {
		if !strings.Contains(pOut, want) {
			t.Errorf("proxy vhost missing %q:\n%s", want, pOut)
		}
	}
}

func TestRegistryRoundTripAndFindByDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sites.toml")
	projectRoot := filepath.Join(t.TempDir(), "blog")
	laravelPublic := filepath.Join(projectRoot, "public")

	reg := Registry{}
	reg.Upsert(Site{
		Name:   "blog",
		Domain: "blog.test",
		Kind:   KindPHP,
		Root:   laravelPublic,
		PHP:    "8.3",
		TLS:    true,
	})
	if err := SaveRegistry(path, reg); err != nil {
		t.Fatalf("SaveRegistry failed: %v", err)
	}

	loaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry failed: %v", err)
	}
	if len(loaded.Sites) != 1 || loaded.Sites[0].Domain != "blog.test" || !loaded.Sites[0].TLS {
		t.Fatalf("unexpected loaded registry: %+v", loaded)
	}

	// Match from project root as well as /public
	if s, ok := loaded.FindByDir(projectRoot); !ok || s.Domain != "blog.test" {
		t.Errorf("FindByDir(projectRoot) = (%+v, %v)", s, ok)
	}
	if s, ok := loaded.FindByDir(laravelPublic); !ok || s.Domain != "blog.test" {
		t.Errorf("FindByDir(laravelPublic) = (%+v, %v)", s, ok)
	}
}

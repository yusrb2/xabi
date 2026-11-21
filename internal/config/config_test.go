package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusrb2/xabi/internal/xdg"
)

func TestParseRoundTripAndComments(t *testing.T) {
	input := `
# Global settings
tld = "local" # inline comment
web_server = 'caddy'
php_default = "8.3"
services = [
  "caddy",
  "php-fpm", # inline
]

[ports]
http = 8080
https = 8443
db = 3307
`
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if cfg.TLD != "local" || cfg.WebServer != "caddy" || cfg.PHPDefault != "8.3" {
		t.Fatalf("unexpected scalar values: %+v", cfg)
	}
	if len(cfg.Services) != 2 || cfg.Services[0] != "caddy" || cfg.Services[1] != "php-fpm" {
		t.Fatalf("unexpected services: %v", cfg.Services)
	}
	if cfg.Ports.HTTP != 8080 || cfg.Ports.HTTPS != 8443 || cfg.Ports.DB != 3307 {
		t.Fatalf("unexpected ports: %+v", cfg.Ports)
	}

	out := cfg.Marshal()
	reparsed, err := Parse([]byte(out))
	if err != nil {
		t.Fatalf("re-parse failed: %v\n%s", err, out)
	}
	if reparsed.TLD != cfg.TLD || reparsed.Ports != cfg.Ports || len(reparsed.Services) != len(cfg.Services) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", reparsed, cfg)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{"bad syntax", "tld \"test\""},
		{"bad web_server", "web_server = \"apache\""},
		{"bad port range", "[ports]\nhttp = 70000"},
		{"duplicate http/https port", "[ports]\nhttp = 80\nhttps = 80"},
		{"unclosed array", "services = [\"nginx\""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.toml)); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestLoadOrDefaultAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	cfg, exists, err := LoadOrDefault(path)
	if err != nil {
		t.Fatalf("LoadOrDefault missing failed: %v", err)
	}
	if exists {
		t.Error("expected exists = false when file is missing")
	}
	if cfg.TLD != "test" || cfg.Ports.HTTP != 80 {
		t.Errorf("unexpected default config: %+v", cfg)
	}

	cfg.TLD = ".dev"
	cfg.Ports.HTTP = 8080
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, exists, err := LoadOrDefault(path)
	if err != nil {
		t.Fatalf("LoadOrDefault existing failed: %v", err)
	}
	if !exists {
		t.Error("expected exists = true after Save")
	}
	if loaded.TLD != "dev" || loaded.Ports.HTTP != 8080 {
		t.Errorf("unexpected loaded config: %+v", loaded)
	}
}

func TestRenderNginxConf(t *testing.T) {
	paths := xdg.Paths{
		ConfigDir:  "/home/alice/.config/xabi",
		DataDir:    "/home/alice/.local/share/xabi",
		RuntimeDir: "/run/user/1000/xabi",
	}

	rendered, err := RenderNginxConf(NewNginxTemplateData(paths, 80))
	if err != nil {
		t.Fatalf("RenderNginxConf failed: %v", err)
	}

	for _, want := range []string{
		"pid /run/user/1000/xabi/nginx.pid;",
		"error_log /run/user/1000/xabi/nginx.log warn;",
		"access_log /run/user/1000/xabi/nginx-access.log;",
		"client_body_temp_path /run/user/1000/xabi/nginx/client_body;",
		"proxy_temp_path       /run/user/1000/xabi/nginx/proxy;",
		"fastcgi_temp_path     /run/user/1000/xabi/nginx/fastcgi;",
		"listen 80 default_server;",
		"include /home/alice/.config/xabi/nginx/sites/*.conf;",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("nginx.conf missing %q", want)
		}
	}
}

package config

import (
	"bytes"
	"path/filepath"
	"runtime"
	"text/template"

	"github.com/yusrb2/xabi/internal/xdg"
)

// WindowsPHPFastCGIAddr is the default TCP loopback address for Windows php-cgi.
const WindowsPHPFastCGIAddr = "127.0.0.1:9000"

// NginxTemplateData holds paths and ports interpolated into the user-space nginx.conf.
type NginxTemplateData struct {
	PIDFile      string
	ErrorLog     string
	AccessLog    string
	MimeTypes    string
	NginxTempDir string
	SitesGlob    string
	PHPFastCGI   string
	HTTPPort     int
}

// NewNginxTemplateData builds NginxTemplateData for the current runtime OS.
func NewNginxTemplateData(paths xdg.Paths, httpPort int) NginxTemplateData {
	return NewNginxTemplateDataForOS(paths, httpPort, runtime.GOOS)
}

// NewNginxTemplateDataForOS builds NginxTemplateData targeting the given GOOS.
func NewNginxTemplateDataForOS(paths xdg.Paths, httpPort int, goos string) NginxTemplateData {
	phpUpstream := "unix:" + filepath.ToSlash(paths.SocketFile("php"))
	if goos == "windows" {
		phpUpstream = WindowsPHPFastCGIAddr
	}
	return NginxTemplateData{
		PIDFile:      filepath.ToSlash(paths.PIDFile("nginx")),
		ErrorLog:     filepath.ToSlash(paths.LogFile("nginx")),
		AccessLog:    filepath.ToSlash(paths.LogFile("nginx-access")),
		MimeTypes:    filepath.ToSlash(filepath.Join(paths.NginxDir(), "mime.types")),
		NginxTempDir: filepath.ToSlash(paths.NginxTempDir()),
		SitesGlob:    filepath.ToSlash(filepath.Join(paths.SitesDir(), "*.conf")),
		PHPFastCGI:   phpUpstream,
		HTTPPort:     httpPort,
	}
}

var nginxConfTmpl = template.Must(template.New("nginx.conf").Parse(`# Managed by xabi — user-space Nginx configuration.
# Edit freely; re-run 'xabi restart nginx' after manual changes.

worker_processes auto;
pid {{.PIDFile}};
error_log {{.ErrorLog}} warn;

events {
    worker_connections 1024;
}

http {
    include {{.MimeTypes}};
    default_type application/octet-stream;

    access_log {{.AccessLog}};

    sendfile on;
    tcp_nopush on;
    keepalive_timeout 65;
    server_names_hash_bucket_size 64;
    client_max_body_size 64m;

    # User-space temp buffers (avoids root-owned /var/lib/nginx paths)
    client_body_temp_path {{.NginxTempDir}}/client_body;
    proxy_temp_path       {{.NginxTempDir}}/proxy;
    fastcgi_temp_path     {{.NginxTempDir}}/fastcgi;
    uwsgi_temp_path       {{.NginxTempDir}}/uwsgi;
    scgi_temp_path        {{.NginxTempDir}}/scgi;

    upstream xabi_php {
        server {{.PHPFastCGI}};
    }

    server {
        listen {{.HTTPPort}} default_server;
        server_name _;
        return 404;
    }

    include {{.SitesGlob}};
}
`))

// RenderNginxConf renders the base user-space nginx.conf from template.
func RenderNginxConf(data NginxTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := nginxConfTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// DefaultMimeTypes returns a compact, self-contained mime.types file for user-space nginx.
func DefaultMimeTypes() string {
	return `types {
    text/html                                        html htm shtml;
    text/css                                         css;
    text/xml                                         xml;
    image/gif                                        gif;
    image/jpeg                                       jpeg jpg;
    application/javascript                           js mjs;
    application/json                                 json;
    text/plain                                       txt;
    image/png                                        png;
    image/svg+xml                                    svg svgz;
    image/webp                                       webp;
    image/avif                                       avif;
    image/x-icon                                     ico;
    font/woff                                        woff;
    font/woff2                                       woff2;
    application/wasm                                 wasm;
    application/pdf                                  pdf;
    application/octet-stream                         bin exe dll deb dmg iso img msi msp msm;
}
`
}

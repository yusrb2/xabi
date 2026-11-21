package vhost

import (
	"bytes"
	"path/filepath"
	"runtime"
	"text/template"

	"github.com/yusrb2/xabi/internal/config"
	"github.com/yusrb2/xabi/internal/xdg"
)

// ServerTemplateData holds normalized forward-slash values rendered into a site's Nginx .conf.
type ServerTemplateData struct {
	Domain    string
	Kind      Kind
	Root      string
	Proxy     string
	FastCGI   string
	HTTPPort  int
	HTTPSPort int
	TLS       bool
	CertPath  string
	KeyPath   string
	IsPHP     bool
	IsStatic  bool
	IsProxy   bool
}

// SiteConfPath returns the Nginx virtual host config path for domain.
func SiteConfPath(paths xdg.Paths, domain string) string {
	return filepath.Join(paths.SitesDir(), domain+".conf")
}

// BuildServerTemplateData constructs ServerTemplateData for the current OS.
func BuildServerTemplateData(site Site, paths xdg.Paths, cfg config.Config) ServerTemplateData {
	return BuildServerTemplateDataForOS(site, paths, cfg, runtime.GOOS)
}

// BuildServerTemplateDataForOS constructs ServerTemplateData targeting the given GOOS.
func BuildServerTemplateDataForOS(site Site, paths xdg.Paths, cfg config.Config, goos string) ServerTemplateData {
	fastCGI := "xabi_php"
	if site.PHP != "" {
		if goos == "windows" {
			fastCGI = config.WindowsPHPFastCGIAddr
		} else {
			fastCGI = "unix:" + filepath.ToSlash(paths.SocketFile("php-"+site.PHP))
		}
	}

	return ServerTemplateData{
		Domain:    site.Domain,
		Kind:      site.Kind,
		Root:      filepath.ToSlash(site.Root),
		Proxy:     site.Proxy,
		FastCGI:   fastCGI,
		HTTPPort:  cfg.Ports.HTTP,
		HTTPSPort: cfg.Ports.HTTPS,
		TLS:       site.TLS,
		CertPath:  filepath.ToSlash(filepath.Join(paths.CertsDir(), site.Domain+".pem")),
		KeyPath:   filepath.ToSlash(filepath.Join(paths.CertsDir(), site.Domain+"-key.pem")),
		IsPHP:     site.Kind == KindPHP,
		IsStatic:  site.Kind == KindStatic,
		IsProxy:   site.Kind == KindProxy,
	}
}

var vhostTmpl = template.Must(template.New("vhost.conf").Parse(`# Managed by xabi — {{.Domain}}
{{if .TLS}}
server {
    listen {{.HTTPPort}};
    server_name {{.Domain}};
    return 301 https://$host{{if ne .HTTPSPort 443}}:{{.HTTPSPort}}{{end}}$request_uri;
}

server {
    listen {{.HTTPSPort}} ssl;
    server_name {{.Domain}};

    ssl_certificate     "{{.CertPath}}";
    ssl_certificate_key "{{.KeyPath}}";
{{else}}
server {
    listen {{.HTTPPort}};
    server_name {{.Domain}};
{{end}}
{{if .IsProxy}}
    location / {
        proxy_pass {{.Proxy}};
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_cache_bypass $http_upgrade;
    }
{{else if .IsPHP}}
    root "{{.Root}}";
    index index.php index.html index.htm;

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location ~ /\.(?!well-known).* {
        deny all;
    }

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        fastcgi_pass {{.FastCGI}};
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param QUERY_STRING    $query_string;
        fastcgi_param REQUEST_METHOD  $request_method;
        fastcgi_param CONTENT_TYPE    $content_type;
        fastcgi_param CONTENT_LENGTH  $content_length;
        fastcgi_param SCRIPT_NAME     $fastcgi_script_name;
        fastcgi_param REQUEST_URI     $request_uri;
        fastcgi_param DOCUMENT_URI    $document_uri;
        fastcgi_param DOCUMENT_ROOT   $document_root;
        fastcgi_param SERVER_PROTOCOL $server_protocol;
        fastcgi_param REQUEST_SCHEME  $scheme;
        fastcgi_param HTTPS           $https if_not_empty;
        fastcgi_param GATEWAY_INTERFACE CGI/1.1;
        fastcgi_param SERVER_SOFTWARE nginx/$nginx_version;
        fastcgi_param REMOTE_ADDR     $remote_addr;
        fastcgi_param REMOTE_PORT     $remote_port;
        fastcgi_param SERVER_ADDR     $server_addr;
        fastcgi_param SERVER_PORT     $server_port;
        fastcgi_param SERVER_NAME     $server_name;
        fastcgi_param REDIRECT_STATUS 200;
    }
{{else}}
    root "{{.Root}}";
    index index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }

    location ~ /\.(?!well-known).* {
        deny all;
    }
{{end}}
}
`))

// RenderNginxVHost renders a complete Nginx server block configuration for site.
func RenderNginxVHost(data ServerTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := vhostTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Brand   string        `yaml:"brand"` // browser tab / page title shown in the UI
	HTTP    HTTPConfig    `yaml:"http"`
	XMPP    XMPPConfig    `yaml:"xmpp"`
	Auth    AuthConfig    `yaml:"auth"`
	Dev     DevConfig     `yaml:"dev"`
	History HistoryConfig `yaml:"history"`
	Log     LogConfig     `yaml:"log"`
}

type HTTPConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	AllowedOrigins  []string      `yaml:"allowed_origins"`
}

type XMPPConfig struct {
	Host                string        `yaml:"host"`
	Port                int           `yaml:"port"`
	Domain              string        `yaml:"domain"`
	TLSMode             string        `yaml:"tls_mode"` // "starttls" verifies the server cert; any other value (e.g. "starttls-insecure", "none") skips verification. STARTTLS is always negotiated either way.
	Resource            string        `yaml:"resource"`
	MUCHost             string        `yaml:"muc_host"`  // primary (local) conference host
	MUCHosts            []string      `yaml:"muc_hosts"` // additional conference hosts to also browse (e.g. remote servers)
	DialTimeout         time.Duration `yaml:"dial_timeout"`
	IdleShutdown        time.Duration `yaml:"idle_shutdown"`
	ReconnectMaxBackoff time.Duration `yaml:"reconnect_max_backoff"`
}

type AuthConfig struct {
	UsernameHeader  string `yaml:"username_header"`
	JIDMappingFile  string `yaml:"jid_mapping_file"`
	CredentialsFile string `yaml:"credentials_file"`
}

type DevConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type HistoryConfig struct {
	DefaultLimit int `yaml:"default_limit"`
	MaxLimit     int `yaml:"max_limit"`
}

type LogConfig struct {
	Level     string `yaml:"level"`      // "debug" | "info" | "warn" | "error"
	Format    string `yaml:"format"`     // "text" | "json" | "syslog"
	File      string `yaml:"file"`       // app log destination; empty = stdout
	AuditFile string `yaml:"audit_file"` // audit log destination; empty = same as app log
}

// Load reads and parses the YAML config file at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	cfg := &Config{}
	if err := yaml.NewDecoder(f).Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Brand == "" {
		c.Brand = "Conduit"
	}
	if c.HTTP.Addr == "" {
		c.HTTP.Addr = ":8080"
	}
	if c.HTTP.ReadTimeout == 0 {
		c.HTTP.ReadTimeout = 10 * time.Second
	}
	if c.HTTP.WriteTimeout == 0 {
		c.HTTP.WriteTimeout = 10 * time.Second
	}
	if c.HTTP.ShutdownTimeout == 0 {
		c.HTTP.ShutdownTimeout = 15 * time.Second
	}
	if c.XMPP.Port == 0 {
		c.XMPP.Port = 5222
	}
	if c.XMPP.Resource == "" {
		c.XMPP.Resource = "conduit"
	}
	if c.XMPP.MUCHost == "" && c.XMPP.Domain != "" {
		c.XMPP.MUCHost = "conference." + c.XMPP.Domain
	}
	if c.XMPP.DialTimeout == 0 {
		c.XMPP.DialTimeout = 10 * time.Second
	}
	if c.XMPP.IdleShutdown == 0 {
		c.XMPP.IdleShutdown = 60 * time.Second
	}
	if c.XMPP.ReconnectMaxBackoff == 0 {
		c.XMPP.ReconnectMaxBackoff = 30 * time.Second
	}
	if c.Auth.UsernameHeader == "" {
		c.Auth.UsernameHeader = "X-Remote-User"
	}
	if c.History.DefaultLimit == 0 {
		c.History.DefaultLimit = 50
	}
	if c.History.MaxLimit == 0 {
		c.History.MaxLimit = 200
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
}

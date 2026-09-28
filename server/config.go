package server

import (
	"github.com/golang-mail/go-mail/cache"
	"github.com/golang-mail/go-mail/plugin"
	"gopkg.in/yaml.v3"
	"os"
)

type Mail struct {
	Subject     string           `json:"subject"`
	UID         uint32           `json:"uid"`
	Body        string           `json:"body"`
	ContentType string           `json:"contentType"`
	From        []*cache.Address `json:"from"`
	To          []*cache.Address `json:"to"`
	Cc          []*cache.Address `json:"cc"`
	Bcc         []*cache.Address `json:"bcc"`
	ReplyTo     []*cache.Address `json:"replyTo"`
	Attachments []string         `json:"attachments"`
}

type MailboxSetting struct {
	Mailbox string `yaml:"mailbox"`
	Icon    string `yaml:"icon"`
}

type FiltersConfig struct {
	// Rearrange rewrites filters.yml in dependency order. The historical key
	// is misspelled; both spellings are accepted.
	Rearrange    bool `yaml:"rearange"`
	RearrangeAlt bool `yaml:"rearrange"`
}

// Rearranged reports whether either spelling enabled rearranging.
func (f FiltersConfig) Rearranged() bool { return f.Rearrange || f.RearrangeAlt }

type TestServerConfig struct {
	Address    string `yaml:"address"`
	Port       int    `yaml:"port"`
	SMTPPort   int    `yaml:"smtp_port"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	TLS        string `yaml:"tls"`
	StorageDir string `yaml:"storage_dir"`
	// Users adds accounts beyond Username.
	Users []TestServerUser `yaml:"users,omitempty"`
	// Seed lists raw message files loaded into mailboxes at first start.
	Seed []TestServerSeed `yaml:"seed,omitempty"`
}

type TestServerUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type TestServerSeed struct {
	Username string   `yaml:"username,omitempty"`
	Mailbox  string   `yaml:"mailbox,omitempty"`
	File     string   `yaml:"file"`
	Flags    []string `yaml:"flags,omitempty"`
}

type ServerConfig struct {
	Username           string           `yaml:"username"`
	Password           string           `yaml:"password"`
	Address            string           `yaml:"address"`
	Port               int              `yaml:"port"`
	DB                 string           `yaml:"db"`
	LogLevel           string           `yaml:"log_level"`
	FilterPeriod       string           `yaml:"filter_period"`
	FilterDeletePeriod string           `yaml:"filter_delete_period"`
	Filters            FiltersConfig    `yaml:"filters"`
	MailboxSettings    []MailboxSetting `yaml:"mailbox_settings"`
	AssetsDir          string           `yaml:"assets_dir"`
	NewMailSound       bool             `yaml:"new_mail_sound"`
	SpamThreshold      float64          `yaml:"spam_threshold"`
	SpamMailbox        string           `yaml:"spam_mailbox"`
	// FilterDebug enables per-filter `[filter-debug]` slog output. Off by default —
	// turn on only when diagnosing filter logic, since it logs every condition
	// check for every email.
	FilterDebug bool `yaml:"filter_debug"`
	// Metrics exposes the Prometheus /metrics endpoint (auth-gated). Off by default.
	Metrics bool `yaml:"metrics"`
	// SecureCookies sets the Secure flag on the session cookie; enable when
	// serving over HTTPS (e.g. behind a TLS-terminating reverse proxy).
	SecureCookies bool              `yaml:"secure_cookies"`
	Test          *TestServerConfig `yaml:"test,omitempty"`
}

func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config ServerConfig
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// RunSelectedRequest describes a request to run filters on specific emails.
type RunSelectedRequest struct {
	Account string
	Mailbox string
	UIDs    []uint32
	Done    chan error
}

// PluginManagerAPI is the slice of plugin.Manager the HTTP layer uses to
// implement /api/plugins read + mutate endpoints. Kept as an interface so
// server tests can stub it out without spawning real plugin processes; main.go
// passes a real *plugin.Manager. Nil means plugins are disabled — the GET
// endpoint returns [] and mutating endpoints reject with 503.
type PluginManagerAPI interface {
	Statuses() []plugin.Status
	GetSpec(name string) (plugin.Spec, bool)
	Specs() *plugin.Config
	ManifestOf(name string) *plugin.Manifest
	ApplyConfig(*plugin.Config) error
	RestartPlugin(name string) error
	TriggerSchedule(name string) error
}

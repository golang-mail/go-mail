package cache

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/client"
	"gopkg.in/yaml.v2"
)

type MailboxConfig struct {
	Order []string `yaml:"order,omitempty"`
}

type Account struct {
	Name     string `yaml:"name"`
	Server   string `yaml:"server"`
	Port     string `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	TLS      string `yaml:"tls"`
	// InsecureSkipVerify disables TLS certificate verification. Opt-in only,
	// for self-signed test servers; leave false in production.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`
	SendOnly           bool `yaml:"send_only,omitempty"`
	// Idle keeps a push connection on INBOX; nil means enabled.
	Idle *bool `yaml:"idle,omitempty"`
	// IdleMailboxes lists mailboxes to watch with IDLE; empty means INBOX.
	IdleMailboxes []string   `yaml:"idle_mailboxes,omitempty"`
	Outgoing      string     `yaml:"outgoing,omitempty"`
	FilterMu      sync.Mutex `yaml:"-"`
	// SentMailbox optionally specifies which mailbox to save sent messages to.
	// If empty, the server will default to using the mailbox that was open in the UI.
	SentMailbox    string              `yaml:"sent_mailbox,omitempty"`
	DraftMailbox   string              `yaml:"draft_mailbox,omitempty"`
	DeletedMailbox string              `yaml:"deleted_mailbox,omitempty"`
	Avatar         string              `yaml:"avatar,omitempty"`
	Gravatar       string              `yaml:"gravatar,omitempty"`
	Signature      string              `yaml:"signature,omitempty"`
	SignatureHTML  string              `yaml:"signature_html,omitempty"`
	SignatureTxt   string              `yaml:"signature_txt,omitempty"`
	FromAddrs      []FromAddress       `yaml:"from_addresses,omitempty" json:"from_addresses,omitempty"`
	Notifications  NotificationsConfig `yaml:"notifications,omitempty"`
	Silence        SilenceConfig       `yaml:"silence,omitempty"`
	MailboxCfg     MailboxConfig       `yaml:"mailbox,omitempty"`
	Mailboxes      map[string]*Mailbox `yaml:"-"`
	pool           connPool
}

type SilenceConfig struct {
	UnseenNotifications []string `yaml:"unseen_notifications,omitempty"`
}

type NotificationsConfig struct {
	Emails bool `yaml:"emails,omitempty" json:"emails"`
	Count  bool `yaml:"count,omitempty" json:"count"`
}

// FromAddress represents a selectable "From" identity for an account.
type FromAddress struct {
	Name      string `yaml:"name" json:"name"`
	Email     string `yaml:"email" json:"email"`
	ReplyTo   string `yaml:"reply_to,omitempty" json:"reply_to,omitempty"`
	Signature string `yaml:"signature,omitempty" json:"signature,omitempty"`
	// Outgoing optionally points to a key in the outgoing_servers map to use when
	// sending mail from this From address.
	Outgoing string `yaml:"outgoing,omitempty" json:"outgoing,omitempty"`
}

// tlsConfig verifies the certificate against the configured server name
// unless insecure_skip_verify is set.
func (a *Account) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName:         a.Server,
		InsecureSkipVerify: a.InsecureSkipVerify, //nolint:gosec // opt-in per account for test setups
	}
}

// dialTimeout bounds the TCP/TLS connect so an unreachable server can't hang
// a handler or a sync for the OS default of minutes.
const dialTimeout = 10 * time.Second

func (a *Account) Connect() (*client.Client, error) {
	addr := fmt.Sprintf("%s:%s", a.Server, a.Port)
	dialer := &net.Dialer{Timeout: dialTimeout}
	var c *client.Client
	var err error
	if a.TLS == "starttls" {
		c, err = client.DialWithDialer(dialer, addr)
		if err != nil {
			return nil, err
		}
		if err := c.StartTLS(a.tlsConfig()); err != nil {
			return nil, err
		}
	} else if a.TLS == "none" {
		c, err = client.DialWithDialer(dialer, addr)
		if err != nil {
			return nil, err
		}
	} else {
		c, err = client.DialWithDialerTLS(dialer, addr, a.tlsConfig())
		if err != nil {
			return nil, err
		}
	}
	if err := c.Login(a.Username, a.Password); err != nil {
		return nil, err
	}
	return c, nil
}

type OutgoingServer struct {
	Server   string `yaml:"server"`
	Port     string `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	TLS      string `yaml:"tls"`
	// InsecureSkipVerify disables TLS certificate verification. Opt-in only,
	// for self-signed test servers; leave false in production.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

type AccountsConfig struct {
	OutgoingServers map[string]OutgoingServer `yaml:"outgoing_servers"`
	Accounts        []*Account                `yaml:"accounts"`
}

// embedExternalImages finds <img> tags with http/https src URLs in the HTML,
// downloads the images, and replaces them with base64 data URIs so they get
// sent as inline CID attachments instead of fragile external references.
var imgSrcRe = regexp.MustCompile(`(<img\s[^>]*src\s*=\s*["'])(https?://[^"']+)(["'])`)

func embedExternalImages(html string) string {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	return imgSrcRe.ReplaceAllStringFunc(html, func(match string) string {
		parts := imgSrcRe.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		prefix, url, quote := parts[1], parts[2], parts[3]

		resp, err := httpClient.Get(url)
		if err != nil {
			slog.Warn("could not download signature image", "url", url, "error", err)
			return match
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slog.Warn("signature image returned non-200", "url", url, "status", resp.StatusCode)
			return match
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			slog.Warn("could not read signature image body", "url", url, "error", err)
			return match
		}
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		dataURI := "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data)
		return prefix + dataURI + quote
	})
}

func LoadAccounts(path string) ([]*Account, map[string]OutgoingServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var config AccountsConfig
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, nil, err
	}
	for i := range config.Accounts {
		if config.Accounts[i].Mailboxes == nil {
			config.Accounts[i].Mailboxes = make(map[string]*Mailbox)
		}
		acc := config.Accounts[i]
		if acc.Signature != "" {
			sigPath := acc.Signature
			if !filepath.IsAbs(sigPath) {
				sigPath = ConfigPath(sigPath)
			}
			content, err := os.ReadFile(sigPath)
			if err != nil {
				slog.Warn("could not read signature file", "file", acc.Signature, "account", acc.Name, "error", err)
			} else {
				if strings.HasSuffix(strings.ToLower(acc.Signature), ".html") || strings.HasSuffix(strings.ToLower(acc.Signature), ".htm") {
					acc.SignatureHTML = embedExternalImages(string(content))
				} else {
					acc.SignatureTxt = string(content)
				}
				acc.Signature = ""
			}
		}
	}
	return config.Accounts, config.OutgoingServers, nil
}

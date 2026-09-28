package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Filter metrics
var (
	FilterRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_filter_runs_total",
		Help: "Total number of filter runs",
	}, []string{"trigger"})

	FilterRunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gomail_filter_run_duration_seconds",
		Help:    "Duration of filter runs",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
	}, []string{"trigger"})

	FiltersRunning = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gomail_filters_running",
		Help: "Whether filters are currently running (0 or 1)",
	})

	FilterActionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_filter_actions_total",
		Help: "Filter actions executed",
	}, []string{"action"})

	FilterMatchedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gomail_filter_matched_emails_total",
		Help: "Emails matched by filters",
	})
)

// Sync metrics
var (
	SyncDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gomail_sync_duration_seconds",
		Help:    "Duration of IMAP sync per account",
		Buckets: prometheus.ExponentialBuckets(0.5, 2, 10),
	}, []string{"account"})

	EmailsSynced = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_emails_synced_total",
		Help: "New emails fetched from IMAP",
	}, []string{"account", "mailbox"})

	IMAPErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_imap_errors_total",
		Help: "IMAP connection and operation errors",
	}, []string{"account", "operation"})
)

// IMAP IDLE (push) metrics
var (
	IdleConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gomail_idle_connections",
		Help: "Live IMAP IDLE connections per account",
	}, []string{"account"})
	IdleEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_idle_events_total",
		Help: "Mailbox changes pushed by IMAP IDLE that triggered a sync",
	}, []string{"account"})
)

// HTTP metrics
var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gomail_http_request_duration_seconds",
		Help:    "HTTP request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// Email operation metrics
var (
	EmailsSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_emails_sent_total",
		Help: "Emails sent via SMTP",
	}, []string{"account", "status"})

	EmailsDeleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gomail_emails_deleted_total",
		Help: "Emails deleted",
	})

	EmailsMoved = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gomail_emails_moved_total",
		Help: "Emails moved between mailboxes",
	})

	DraftsSaved = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gomail_drafts_saved_total",
		Help: "Drafts saved",
	})

	LoginAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gomail_login_attempts_total",
		Help: "Login attempts",
	}, []string{"result"})

	SearchQueries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gomail_search_queries_total",
		Help: "Search queries executed",
	})
)

// Cache state metrics (set periodically)
var (
	CachedEmails = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gomail_cached_emails",
		Help: "Number of emails in cache",
	}, []string{"account", "mailbox"})

	UnseenEmails = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gomail_unseen_emails",
		Help: "Number of unseen emails",
	}, []string{"account", "mailbox"})

	AccountsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gomail_accounts_total",
		Help: "Number of configured accounts",
	})

	MailboxesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gomail_mailboxes_total",
		Help: "Number of mailboxes per account",
	}, []string{"account"})

	ActiveSessions = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gomail_active_sessions",
		Help: "Number of active login sessions",
	})
)

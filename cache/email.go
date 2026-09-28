package cache

import "slices"

// CachedEmail is a serializable representation of an email message.
type CachedEmail struct {
	UID         uint32            `yaml:"uid" json:"uid"`
	Envelope    *Envelope         `yaml:"envelope" json:"envelope"`
	Body        string            `yaml:"body" json:"body"`
	ContentType string            `yaml:"content_type" json:"contentType"`
	Flags       []string          `yaml:"flags" json:"flags"`
	Attachments []string          `yaml:"attachments" json:"attachments"`
	Headers     map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Labels      []string          `yaml:"labels,omitempty" json:"labels,omitempty"`
	// PluginsRun records which filter plugins have already executed for this email.
	// Persisted across restarts so plugins (e.g. slack-notify) don't re-fire on Run All.
	PluginsRun []string `yaml:"plugins_run,omitempty" json:"pluginsRun,omitempty"`
	Filtered   bool     `yaml:"filtered" json:"filtered"`
}

// Snapshot copies the email with its mutable slices cloned, for lock-free reads.
// Envelope, Headers and Body are never mutated in place, so they are shared.
func (e *CachedEmail) Snapshot() *CachedEmail {
	s := *e
	s.Flags = slices.Clone(e.Flags)
	s.Labels = slices.Clone(e.Labels)
	s.PluginsRun = slices.Clone(e.PluginsRun)
	return &s
}

// Env returns the envelope, or an empty one for rows that lack it.
func (e *CachedEmail) Env() *Envelope {
	if e.Envelope == nil {
		return &Envelope{}
	}
	return e.Envelope
}

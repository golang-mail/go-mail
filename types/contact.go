package types

// Contact represents a single email contact.
type Contact struct {
	Email     string   `yaml:"email"`
	Mailboxes []string `yaml:"mailboxes"`
}

// ContactsConfig represents the structure of the contacts.yaml file.
type ContactsConfig struct {
	IgnoreMailboxes []string                        `yaml:"ignore_mailboxes"`
	Groups          map[string]map[string][]Contact `yaml:"groups"`
}

package cache

type Mailbox struct {
	Name string `yaml:"name" json:"name"`
	// Messages is the total number of emails known in this mailbox,
	// including ones still pending filter evaluation.
	Messages uint32 `yaml:"messages" json:"messages"`
	// Unseen counts emails with Filtered==true AND no \Seen flag.
	// Maintained incrementally by every flag/move/delete/sync/filter site.
	Unseen uint32                  `yaml:"unseen" json:"unseen"`
	Emails map[uint32]*CachedEmail `yaml:"-" json:"-"`
}

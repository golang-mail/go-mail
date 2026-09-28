package cache

import "time"

// Address is a serializable representation of an email address.
type Address struct {
	PersonalName string `yaml:"personal_name" json:"PersonalName"`
	MailboxName  string `yaml:"mailbox_name" json:"MailboxName"`
	HostName     string `yaml:"host_name" json:"HostName"`
}

// Envelope is a serializable representation of an email envelope.
type Envelope struct {
	Date      time.Time  `yaml:"date" json:"Date"`
	Subject   string     `yaml:"subject" json:"Subject"`
	From      []*Address `yaml:"from" json:"From"`
	Sender    []*Address `yaml:"sender" json:"Sender"`
	ReplyTo   []*Address `yaml:"reply_to" json:"ReplyTo"`
	To        []*Address `yaml:"to" json:"To"`
	Cc        []*Address `yaml:"cc" json:"Cc"`
	Bcc       []*Address `yaml:"bcc" json:"Bcc"`
	InReplyTo string     `yaml:"in_reply_to" json:"InReplyTo"`
	MessageID string     `yaml:"message_id" json:"MessageId"`
}

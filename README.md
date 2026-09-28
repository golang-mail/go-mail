# Go-Mail

A self-hosted web mail client in a single Go binary. It keeps a local copy of
your IMAP accounts, sorts mail with server-side filters and plugins, and serves
a fast Gmail-style web UI. New mail arrives by IMAP push, not polling.

- **Multiple IMAP accounts** cached locally in SQLite (or YAML, or memory).
  Reading, searching and listing never wait on the mail server.
- **Filters** in a YAML file with dependency ordering, run on new mail as it
  arrives and on a schedule. Conditions on any header, age, labels, plugin
  verdicts; actions move, label, flag, delete, mark as spam.
- **Plugins** as separate processes (any language) that classify mail, post
  notifications, auto-reply, or fetch labels from external systems.
- **Push** via IMAP IDLE with server-sent events to the browser.
- **Search** with Gmail-style operators, negation, and a full-text index.
- **Compose** in rich text or plain text with attachments, drafts, signatures
  and per-address identities. Calendar invites can be answered in place.
- **Labels** stored locally or as IMAP keywords, a trainable spam classifier
  with a safe list, Prometheus metrics and a Grafana dashboard.

## Quick start

Requires Go 1.26 or newer.

```bash
git clone https://github.com/golang-mail/go-mail && cd go-mail
go build -o go-mail .
./go-mail hash-password           # type a password, paste the hash below
mkdir -p ~/.go-mail
```

`~/.go-mail/server.yml`:

```yaml
username: me
password: "$2a$10$..."             # bcrypt hash from hash-password, or clear text
address: 127.0.0.1
port: 9090
```

`~/.go-mail/accounts.yml`:

```yaml
outgoing_servers:
  main:
    server: smtp.example.com
    port: "587"
    username: me@example.com
    password: app-password
    tls: starttls

accounts:
  - name: Personal
    server: imap.example.com
    port: "993"
    username: me@example.com
    password: app-password
    tls: tls
    outgoing: main
    sent_mailbox: Sent
    draft_mailbox: Drafts
    deleted_mailbox: Trash
    from_addresses:
      - name: My Name
        email: me@example.com
```

Then `./go-mail` and open `http://127.0.0.1:9090`. The first start syncs every
mailbox into `~/.go-mail/db/`; the UI is usable while that runs.

Put it behind a TLS-terminating reverse proxy for anything beyond localhost
and set `secure_cookies: true`.

## Configuration

Everything lives in `~/.go-mail`.

### server.yml

| Key | Default | Meaning |
|---|---|---|
| `username`, `password` | required | Web login. `password` may be a bcrypt hash from `go-mail hash-password` (`$2a$`/`$2b$`/`$2y$` prefix) or clear text. |
| `address`, `port` | required | Listen address. There is no default port. |
| `db` | `sqlite` | Cache backend: `sqlite`, `yaml`, or `memory` (bodies re-fetched on restart; labels and state kept in a sidecar). |
| `log_level` | `info` | `debug`, `info`, `warn`, `error`. |
| `filter_period` | | Pause between periodic filter runs, e.g. `5m`. Push handles new mail; this catches everything else. |
| `filter_delete_period` | | Pause between runs of age-based delete filters only. |
| `filters.rearrange` | `false` | Rewrite `filters.yml` in execution order after each load. |
| `mailbox_settings` | | List of `{mailbox, icon}` for the sidebar. |
| `assets_dir` | | Directory under `~/.go-mail` served (to logged-in users) for avatars and icons. |
| `new_mail_sound` | `false` | Default for the new-mail chime toggle. |
| `spam_threshold`, `spam_mailbox` | `0.9`, `Junk` | Spam classifier cutoff and destination. Scores only move mail once 20 spam and 20 ham examples are trained. |
| `filter_debug` | `false` | Log every condition evaluation. |
| `metrics` | `false` | Expose `/metrics` (Prometheus, login required). |
| `secure_cookies` | `false` | Set the `Secure` flag on the session cookie. Turn on behind HTTPS. |
| `test` | | Start an embedded IMAP/SMTP server for local trials: `address`, `port`, `smtp_port`, `username`, `password`, `tls`, `storage_dir`, `push`. |

### accounts.yml

`outgoing_servers` is a map of SMTP servers: `server`, `port`, `username`,
`password`, `tls` (`tls`, `starttls`, `none`), and `insecure_skip_verify` to
accept self-signed certificates.

Each entry in `accounts`:

| Key | Meaning |
|---|---|
| `name` | Display name, used in URLs and filters. |
| `server`, `port`, `username`, `password`, `tls` | IMAP connection. `tls` is `tls`, `starttls` or `none`. |
| `send_only` | Skip IMAP sync; only used as a sending identity. |
| `outgoing` | Key into `outgoing_servers`. Overridable per from address. |
| `sent_mailbox`, `draft_mailbox`, `deleted_mailbox` | Special mailboxes, created and subscribed on startup when missing. |
| `from_addresses` | List of `{name, email, reply_to, signature, outgoing}` identities offered in compose. |
| `signature`, `signature_html`, `signature_txt` | Default signatures. |
| `avatar`, `gravatar` | Sidebar avatar: a file under `assets_dir` or an address for Gravatar. |
| `idle` | `false` disables IMAP IDLE push for this account. |
| `idle_mailboxes` | Mailboxes to watch with IDLE; default `[INBOX]`. One connection each. |
| `notifications` | `{emails: true, count: true}` browser notifications. |
| `silence.unseen_notifications` | Mailboxes excluded from unseen counts and notifications. |
| `mailbox.order` | Sidebar order for mailbox names. |

### filters.yml

See [filter.md](filter.md) for the full reference. A filter:

```yaml
filters:
  - name: Newsletters
    mailboxes: [INBOX]            # globs allowed, default INBOX
    conditions_type: and          # or "or"
    conditions:
      - field: header:List-Unsubscribe
        operator: exists
      - field: from
        operator: not-contains
        value: "@work.example"
    actions:
      - type: move
        value: Lists
      - type: mark_as_read
```

Fields: `from`, `sender`, `to`, `cc`, `bcc`, `from/to`, `from/to/cc/bcc`,
`subject`, `body`, `age`, `status`, `label`, `header:<Name>`,
`plugin:<name>`. Operators: `equals`, `contains`, `not-contains`,
`begins_with`, `ends_with`, `matches` (regex), `exists` (headers only),
`older_than` (for `age`, e.g. `30d`, `2w`, `6m`, `1y`). Actions: `move`,
`delete`, `permanent_delete`, `mark_as_read`, `mark_as_unread`, `flag`,
`label`, `mark_as_spam`, `plugin_clear_cache`.

A filter that moves mail into a mailbox always runs before filters that read
that mailbox. Filters can also be edited in the UI; the API rejects unknown
fields, operators and actions.

If your mail server already runs SpamAssassin or rspamd, a `header:` condition
with `matches` on its score header is the simplest spam filter, for example
`header:X-Spam-Status` matching `^Yes` moved to `Junk`.

### labels.yml

```yaml
labels:
  - name: Work
    color: "#1a73e8"
    storage: imap        # or local (cache only)
    accounts: [Personal] # optional scope
    hidden: false
    weight: 10           # chip order
```

Labels used by filters are registered automatically.

### plugins.yml

```yaml
plugins:
  - name: slack-notify
    command: [slack-notify]
    config:
      webhook: https://hooks.slack.com/...
```

Plugins are child processes speaking newline-delimited JSON over a Unix
socket (`transport: stdio` also works, and `address:` points at an already
running plugin). A plugin declares what it wants (`envelope`, `headers`,
`body`), which actions it may return, and which host calls it needs
(`host_methods`). The host remembers which plugins already ran on a message
so notifications fire once. The wire protocol is documented in
[`plugin/README.md`](plugin/README.md). Plugins live in their own repos under
the [golang-mail](https://github.com/golang-mail) org — Slack, Discord,
Telegram and ntfy notifiers, a generic webhook, newsletter and phishing
detection, an auto-replier, AI summaries and GitLab label sync — each
installed with `go install`. Secrets in `config` are masked in the plugin
editor.

## Search

Type in the search box. Words match as prefixes against subject, body and
addresses. Operators narrow the result and combine with AND:

```
from:alice to:me subject:"weekly report" label:work has:attachment
is:unread older_than:30d newer_than:2w -from:noreply -has:attachment
```

A leading `-` negates an operator. Words that contain regex syntax such as
`.` `*` `|` or `^` are treated as a case-insensitive regular expression
instead of an index lookup. The scope toggle switches between the current
mailbox and everything.

## How it works

Each account is synced into the local cache: envelopes, bodies, headers and
attachment names. Filters run on the cache and apply their actions over one
IMAP connection per account. An IMAP IDLE connection per watched mailbox
triggers a sync and filter pass within a couple of seconds of new mail, and a
server-sent event refreshes open browsers. The periodic run covers mailboxes
without IDLE and servers that lack it.

The spam classifier learns from the Report spam and Not spam buttons. It
scores sender address and domain, reply-to mismatch, authentication results,
list headers, subject and body words with SpamBayes-style token odds combined
through Fisher's chi-square test, so an unbalanced training set or unfamiliar
vocabulary does not bias it. After 20 examples of each class it moves INBOX
mail above `spam_threshold` to `spam_mailbox`, skipping safe-listed senders.

The web UI is a single page of vanilla JavaScript served from the binary.
Every response carries a Content-Security-Policy with a per-request script
nonce, session cookies are HttpOnly and SameSite strict, login is rate
limited per IP, and HTML mail is sanitized with DOMPurify before rendering.

## Monitoring

Set `metrics: true` and point Prometheus at `/metrics` with the session
cookie, or run the bundled stack:

```bash
cd monitoring && docker compose up
```

It ships a Prometheus config and a Grafana dashboard for filter runs, sync
timings, IMAP errors, HTTP latency, IDLE connections and cache sizes.

## Development

```bash
go test ./...                      # unit tests, including the in-process IMAP/SMTP server
CGO_ENABLED=1 go test -race ./...  # locking verified under the race detector
go vet ./... && staticcheck ./...
cd e2e && npm run setup && npm test   # Playwright: security, filters, compose, invites
```

The browser suite boots the app against the embedded test server in a
throwaway directory and never touches `~/.go-mail`. Agent-oriented notes on
the code layout, conventions, and functional overview are in [AGENTS.md](AGENTS.md).

## License

See [LICENSE](LICENSE).

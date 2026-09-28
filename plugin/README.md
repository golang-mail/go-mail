# go-mail plugin protocol

go-mail runs each plugin as a separate, supervised process. Plugins can be
written in any language: the wire protocol is newline-delimited JSON (NDJSON),
one `Envelope` object per line.

The host spawns the process (or dials an already-running one), performs an
`init` handshake to learn the plugin's manifest, calls `evaluate` for each
email, may send `batch_complete` / `scheduled_tick` events, and finally
`shutdown`. Plugins may call back into the host through `host.*` RPC.

## Transports

Selected per plugin via `transport:` in `plugins.yml`.

| transport | behaviour |
|---|---|
| `socket` (default) | The host allocates a Unix socket, exports its path in `GO_MAIL_PLUGIN_SOCKET`, spawns `command`, then dials it. |
| `stdio` | NDJSON over the child's stdin/stdout; the child's stderr is forwarded to the host log. |
| external | No `command:`; set `address: unix:///path` to dial an already-running plugin. TCP is not implemented. |

Socket path: `$XDG_RUNTIME_DIR/go-mail/plugins/<binary>-<hash>.sock`, falling
back to `$TMPDIR/go-mail-<uid>/go-mail/plugins/`.

Specs sharing an identical `command` share one OS process, but each spec opens
its own connection with its own `init` handshake and config. Plugins that
support this must keep per-connection state, keyed on the connection.

## Envelope

Every message is one JSON object on one line:

```json
{"id":"...","type":"request","method":"evaluate","payload":{...}}
```

| field | meaning |
|---|---|
| `id` | Correlation id. A `response` must echo the `id` of the request it answers. |
| `type` | `request`, `response`, or `event`. |
| `method` | `init`, `evaluate`, `shutdown`, `host.<name>`, `batch_complete`, `scheduled_tick`, … |
| `payload` | Method-specific JSON object; omitted when empty. |

## Handshake

Once per connection, the host sends:

```json
{"id":"1","type":"request","method":"init","payload":{"host_version":"...","config":{}}}
```

The plugin must answer with its manifest:

```json
{"id":"1","type":"response","method":"init","payload":{"name":"...","version":"..."}}
```

`config` is the operator's `plugins.yml` `config:` block re-encoded as JSON.

### Manifest

| field | meaning |
|---|---|
| `name` | Plugin name shown in the UI. Defaults to the `plugins.yml` name when empty. |
| `version` | Plugin version; part of the verdict-cache key. |
| `actions` | Action types this plugin may return (declarative; surfaced to the UI). |
| `wants` | Per-email fields to serialize: any of `envelope`, `headers`, `body`. |
| `cacheable` | Host caches the verdict per `(plugin, version, account, mailbox, uid, filter_config)`. |
| `run_last` | Default `true`. Defers filters referencing this plugin until non-plugin filters have run, so cleanup (`move`/`delete`) happens before side-effecting plugins. |
| `rerunnable` | Default `false`. Disables the host's run-once-per-email guard. |
| `rescan_interval` | Go duration (`1h`). With `rerunnable`, re-evaluate this plugin's filters over the whole mailbox scope periodically. |
| `wants_batch_complete` | Receive a `batch_complete` event after every filter run. |
| `wants_mailbox_state` | Extend `batch_complete` with a mailbox snapshot (honours `wants`). |
| `host_methods` | `host.*` methods this plugin may call, without the `host.` prefix. Empty = no host RPC. |
| `wants_schedule` | Receive `scheduled_tick` events. |
| `schedule` | Tick cadence: a Go duration (`10m`) or cron (`@daily`, `0 8 * * 1-5`). |

## evaluate

The host asks the plugin to judge one email:

```json
{
  "id": "2",
  "type": "request",
  "method": "evaluate",
  "payload": {
    "email": {
      "uid": 42,
      "account": "personal",
      "mailbox": "INBOX",
      "envelope": { "...": "..." },
      "headers": { "...": "..." },
      "body": "...",
      "content_type": "text/html",
      "flags": ["\\Seen"],
      "labels": ["work"],
      "date": "2026-01-02T15:04:05Z"
    },
    "filter_config": "general",
    "plugin_config": { "...": "..." }
  }
}
```

- `email.envelope`, `email.headers` and `email.body` are present only when the
  manifest's `wants` opted into them.
- `filter_config` is the `value:` of the `plugin:<name>` condition in the filter.
- `plugin_config` is the same opaque config sent at `init`.

The plugin answers:

```json
{"id":"2","type":"response","method":"evaluate","payload":{"matched":true,"actions":[{"type":"label","value":"newsletter"}],"reason":"list-unsubscribe"}}
```

| field | meaning |
|---|---|
| `matched` | Whether the `plugin:<name>` condition matches. |
| `actions` | Actions to apply when matched (see [Actions](#actions)). |
| `reason` | Human-readable explanation, shown as the condition's value in logs and the UI. |
| `error` | Non-empty means the evaluation failed; the host logs it as an error. |

An empty payload (`{}`) means "no match, no actions".

## Host-initiated events

Events use `type:"event"` and expect no response. Unknown methods must be
ignored silently.

| method | requires | payload |
|---|---|---|
| `batch_complete` | `wants_batch_complete` | `run_id`, `started_at`, `ended_at`, `trigger` (`startup` \| `periodic` \| `watcher` \| `manual` \| `rescan`), `mailboxes` (`[{account, mailbox}]`), `counts` (`{matched, processed}`), and `mailbox_state` when `wants_mailbox_state` is set. |
| `scheduled_tick` | `wants_schedule` + `schedule` | `fired_at`, `schedule`, `since` (previous fire, zero on first), `force` (true for an operator-triggered "Run Now" — bypass change-detection gating). |

`mailbox_state` is `[{account, mailbox, emails: [EvalEmail]}]`, where each email
honours the plugin's `wants`.

## Host RPC

A plugin calls the host by sending a `request`; the host replies with a
`response` carrying the same `id`:

```json
{"id":"9","type":"request","method":"host.list_emails","payload":{"account":"personal","mailbox":"INBOX"}}
```

```json
{"id":"9","type":"response","method":"host.list_emails","payload":{"emails":[],"total":0,"page":0,"page_size":0}}
```

On failure the payload is an error document, e.g.
`{"error":"rate_limited","retry_after_ms":500}` or `{"error":"method not granted: host.search"}`.

Except for `host.log`, every method must be listed in the manifest's
`host_methods`, and calls are rate-limited per plugin (default 2 QPS; set
`host_call_qps` in `plugins.yml`, a negative value disables the limit).

| method | request | response |
|---|---|---|
| `host.log` | `{level, msg, fields}` — always available, no grant, no rate limit | `{}` |
| `host.list_accounts` | — | `{accounts: [{name, unseen, mailbox_count, avatar}]}` |
| `host.list_mailboxes` | `{account}` | `{mailboxes: [{name, unseen, total}]}` |
| `host.list_emails` | `{account, mailbox, page, page_size, unread, since}` | `{emails: [EmailRef], total, page, page_size}` |
| `host.get_email` | `{account, mailbox, uid, parts}` | `{uid, account, mailbox, envelope, headers, body, content_type, flags, labels, date}` |
| `host.search` | `{account, mailbox, query, with_body}` | `{emails: [EmailRef]}` |
| `host.list_labels` | — | `{labels: [{name, hidden, storage, color}]}` |

`EmailRef` is the slim per-email record: `{uid, account, mailbox, envelope,
flags, labels, date}` (no body).

- `host.list_emails`: `page` is 1-indexed; `page` 0 means unpaginated (capped at
  5000). `unread` filters out `\Seen`; `since` drops older emails.
- `host.get_email`: `parts` is a subset of `envelope`, `headers`, `body`;
  defaults to `["envelope"]`.
- `host.search`: `query` is a case-insensitive regex over subject and addresses
  (and body when `with_body` is true).
- `host.list_labels`: `hidden` marks labels the UI suppresses.

## Actions

Returned actions are applied only when `matched` is true. `value` is required
for `move`, `flag` and `label`.

| type | value | effect |
|---|---|---|
| `move` | mailbox | Move the message. |
| `delete` | — | Move to Trash. |
| `permanent_delete` | — | Delete permanently. |
| `mark_as_read` | — | Set `\Seen`. |
| `mark_as_unread` | — | Clear `\Seen`. |
| `flag` | flag name | Set an IMAP flag / keyword. |
| `label` | label name | Apply a go-mail label. |
| `mark_as_spam` | — | Mark as spam. |
| `plugin_clear_cache` | — | Clear the host's verdict cache. |

## Shutdown

The host sends:

```json
{"id":"3","type":"request","method":"shutdown","payload":{}}
```

The plugin should answer, then exit. The host then closes the transport; for
socket plugins the process is signalled `SIGINT` and killed after a short grace
period. Stdio plugins treat stdin EOF as the exit signal; socket plugins serve
connections and should exit on `shutdown` (or when the host terminates the
process).

## Supervision

`plugins.yml` controls the process lifecycle per plugin:

| key | default | meaning |
|---|---|---|
| `timeout` | `5s` | Per-request deadline (init, evaluate, shutdown, host RPC). |
| `dial_timeout` | `5s` | Socket dial / readiness timeout. |
| `max_restarts` | `5` | Consecutive failures before the plugin is disabled. |
| `host_call_qps` | `2` | Host RPC rate limit; negative disables. |

## Reference implementations

Runnable examples live in [`testdata/`](testdata): `stdio-noop` (minimal stdio
plugin), `host-caller` (host RPC), `batch-watcher` (`batch_complete`). The
plugins in the [`golang-mail`](https://github.com/golang-mail) org are
real-world references, starting with
[`subject-noop`](https://github.com/golang-mail/subject-noop).

## Minimal transcript

```jsonl
-> {"id":"1","type":"request","method":"init","payload":{"host_version":"dev"}}
<- {"id":"1","type":"response","method":"init","payload":{"name":"subject-noop","version":"1.0.0","actions":[],"wants":["envelope"]}}
-> {"id":"2","type":"request","method":"evaluate","payload":{"email":{"uid":42,"account":"personal","mailbox":"INBOX","envelope":{"Subject":"hi"},"date":"2026-01-02T15:04:05Z"},"filter_config":""}}
<- {"id":"2","type":"response","method":"evaluate","payload":{}}
-> {"id":"3","type":"request","method":"shutdown","payload":{}}
<- {"id":"3","type":"response","method":"shutdown","payload":{}}
```

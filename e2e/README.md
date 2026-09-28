# go-mail end-to-end tests

Browser-level tests (Playwright) for security, the filters/labels/plugins
editors, compose and delivery, and calendar RSVP. Each run builds the app once,
then boots a fresh instance per spec file against a throwaway config dir with
an in-process IMAP/SMTP test server seeded from `fixtures/`, and drives a
headless Chromium against it.

## Setup (once)

```bash
cd e2e
npm run setup    # installs @playwright/test and downloads Chromium
```

Requires `go` on PATH (the suite builds the binary).

## Run

```bash
cd e2e
npm test                       # or: npx playwright test
npx playwright test tests/security.spec.js:61   # a single test by line
```

`lib/global-setup.js` builds the binary into a temp root dir. Every spec file
calls `useApp()` from `lib/app.js`, which boots its own app before the file's
tests (own `HOME`, mail store and ports, so your real `~/.go-mail` is never
touched and files never share INBOX state) and stops it afterwards. Ports are
reserved in JS before the app binds them; a lost race ("address already in
use" in the log) is retried with fresh ports up to three times.
`lib/global-teardown.js` stops anything left running and removes the root dir;
set `E2E_KEEP=1` to keep it for debugging (each `app-*/app.log` has that
file's server output). `E2E_LOG_LEVEL` sets the app's `log_level` (default
`error`).

## Writing specs

Import `test` and `expect` from `../lib/app`, never from `@playwright/test`,
and call `useApp()` once at file scope. Tests receive `rt` (base URL,
credentials, account name) as a fixture next to `page`.

Headless Chromium in this environment never fires `requestAnimationFrame`, so
Playwright's real `page.click`/`check` (which wait for the element to be
stable) hang until timeout. The extended `page` fixture therefore rewrites
`page.click`, `page.dblclick`, `page.check` and `page.uncheck`, and the same
methods on that page's locators, to dispatch the DOM event directly (`check`
dispatches a click and then asserts the checked state). The events still
bubble to the delegated `data-action` listener exactly like a user click, so
specs read like ordinary Playwright without hanging.

## Seed mail

`fixtures/*.eml` are raw RFC 5322 messages (CRLF line endings) appended to
INBOX in the order listed in `lib/app.js`, so `xss.eml` is UID 1 and
`invite.eml` UID 2. The canary string in `xss.eml` must match `CANARY` in
`lib/harness.js`.

## What's covered

- A malicious plain-text email body renders as inert text, not live DOM (XSS).
- The session cookie is `HttpOnly` and unreadable from `document.cookie`.
- Responses carry a Content-Security-Policy and related headers; the SPA runs
  without CSP violations.
- `/metrics` is disabled unless enabled in config.
- Filters: create through the editor, summary cards, disabled/scheduled filters.
- Labels and plugins lists; plugin enable/disable toggle.
- Compose sends through SMTP, the message arrives via IDLE push, and the Sent copy is filed.
- Accepting a calendar invite sends the iTIP reply and files it in Sent.

## Layout

- `lib/global-setup.js` — build the binary, write `.runtime.json`.
- `lib/global-teardown.js` — stop leftover apps, clean up.
- `lib/harness.js` — shared paths, free-port and process helpers, the payload canary.
- `lib/app.js` — `useApp`/`bootApp`/`stopApp`, the extended `test`, `login`/`click`/`openMail`.
- `fixtures/` — seed messages.
- `tests/*.spec.js` — the specs.

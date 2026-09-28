package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var sqliteStatements = []string{
	`CREATE TABLE IF NOT EXISTS mailboxes (
		name     TEXT PRIMARY KEY,
		messages INTEGER NOT NULL DEFAULT 0,
		unseen   INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS emails (
		uid            INTEGER NOT NULL,
		mailbox        TEXT    NOT NULL,
		subject        TEXT    NOT NULL DEFAULT '',
		date           TEXT    NOT NULL DEFAULT '',
		message_id     TEXT    NOT NULL DEFAULT '',
		in_reply_to    TEXT    NOT NULL DEFAULT '',
		from_addrs     TEXT    NOT NULL DEFAULT '[]',
		sender_addrs   TEXT    NOT NULL DEFAULT '[]',
		reply_to_addrs TEXT    NOT NULL DEFAULT '[]',
		to_addrs       TEXT    NOT NULL DEFAULT '[]',
		cc_addrs       TEXT    NOT NULL DEFAULT '[]',
		bcc_addrs      TEXT    NOT NULL DEFAULT '[]',
		body           TEXT    NOT NULL DEFAULT '',
		content_type   TEXT    NOT NULL DEFAULT '',
		flags          TEXT    NOT NULL DEFAULT '[]',
		attachments    TEXT    NOT NULL DEFAULT '[]',
		headers        TEXT    NOT NULL DEFAULT '{}',
		labels         TEXT    NOT NULL DEFAULT '[]',
		plugins_run    TEXT    NOT NULL DEFAULT '[]',
		filtered       INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (uid, mailbox)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_emails_mailbox ON emails(mailbox)`,
}

// SQLiteBackend persists cache data in SQLite databases (one per account).
type SQLiteBackend struct {
	baseDir string
	mu      sync.Mutex
	dbs     map[string]*sql.DB
	// emailHashes[account][mailbox][uid] -> sha256 of last-written row content;
	// only rows whose hash changed are re-INSERT/UPDATE'd, and rows missing from
	// the input get DELETE'd by uid. First save after restart writes everything once.
	emailHashes map[string]map[string]map[uint32][32]byte
	// mailboxHashes[account][name] -> hash of last-written mailbox row.
	mailboxHashes map[string]map[string][32]byte
}

// NewSQLiteBackend creates a new SQLite-based cache backend.
func NewSQLiteBackend(baseDir string) *SQLiteBackend {
	return &SQLiteBackend{
		baseDir:       baseDir,
		dbs:           make(map[string]*sql.DB),
		emailHashes:   make(map[string]map[string]map[uint32][32]byte),
		mailboxHashes: make(map[string]map[string][32]byte),
	}
}

func (b *SQLiteBackend) getDB(accountName string) (*sql.DB, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if db, ok := b.dbs[accountName]; ok {
		return db, nil
	}

	accountDir := fmt.Sprintf("%s/%s", b.baseDir, accountName)
	if err := os.MkdirAll(accountDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating account directory: %w", err)
	}

	dbPath := fmt.Sprintf("%s/cache.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", accountDir)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	// SQLite only supports one writer at a time.
	db.SetMaxOpenConns(1)

	for _, stmt := range sqliteStatements {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("creating schema: %w", err)
		}
	}

	// Migration: add filtered column to existing databases
	_, _ = db.Exec("ALTER TABLE emails ADD COLUMN filtered INTEGER NOT NULL DEFAULT 0")
	// Migration: add plugins_run column to existing databases (filter plugins per-email tracking)
	_, _ = db.Exec("ALTER TABLE emails ADD COLUMN plugins_run TEXT NOT NULL DEFAULT '[]'")

	if err := ensureTextIndex(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating text index: %w", err)
	}

	slog.Info("SQLite cache opened", "account", accountName, "path", accountDir+"/cache.db")
	b.dbs[accountName] = db
	return db, nil
}

func (b *SQLiteBackend) LoadMailboxes(accountName string) (map[string]*Mailbox, error) {
	db, err := b.getDB(accountName)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query("SELECT name, messages, unseen FROM mailboxes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mailboxes := make(map[string]*Mailbox)
	for rows.Next() {
		var m Mailbox
		if err := rows.Scan(&m.Name, &m.Messages, &m.Unseen); err != nil {
			return nil, err
		}
		mailboxes[m.Name] = &m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	primed := make(map[string][32]byte, len(mailboxes))
	for name, m := range mailboxes {
		primed[name] = hashMailboxRow(m)
	}
	b.mu.Lock()
	b.mailboxHashes[accountName] = primed
	b.mu.Unlock()

	return mailboxes, nil
}

func (b *SQLiteBackend) LoadEmails(accountName, mailboxName string) (map[uint32]*CachedEmail, error) {
	db, err := b.getDB(accountName)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`SELECT uid, subject, date, message_id, in_reply_to,
		from_addrs, sender_addrs, reply_to_addrs, to_addrs, cc_addrs, bcc_addrs,
		body, content_type, flags, attachments, headers, labels, plugins_run, filtered
		FROM emails WHERE mailbox = ?`, mailboxName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	emails := make(map[uint32]*CachedEmail)
	for rows.Next() {
		var e CachedEmail
		var dateStr string
		var fromJSON, senderJSON, replyToJSON, toJSON, ccJSON, bccJSON string
		var flagsJSON, attachJSON, headersJSON, labelsJSON, pluginsRunJSON string
		var subject, messageID, inReplyTo string
		var filteredInt int

		if err := rows.Scan(
			&e.UID, &subject, &dateStr, &messageID, &inReplyTo,
			&fromJSON, &senderJSON, &replyToJSON, &toJSON, &ccJSON, &bccJSON,
			&e.Body, &e.ContentType,
			&flagsJSON, &attachJSON, &headersJSON, &labelsJSON, &pluginsRunJSON, &filteredInt,
		); err != nil {
			return nil, err
		}

		date, err := time.Parse(time.RFC3339, dateStr)
		if err != nil {
			slog.Warn("sqlite cache: bad date column", "value", dateStr, "error", err)
		}
		e.Envelope = &Envelope{
			Date:      date,
			Subject:   subject,
			MessageID: messageID,
			InReplyTo: inReplyTo,
			From:      unmarshalAddresses(fromJSON),
			Sender:    unmarshalAddresses(senderJSON),
			ReplyTo:   unmarshalAddresses(replyToJSON),
			To:        unmarshalAddresses(toJSON),
			Cc:        unmarshalAddresses(ccJSON),
			Bcc:       unmarshalAddresses(bccJSON),
		}
		e.Flags = unmarshalStrings(flagsJSON)
		e.Attachments = unmarshalStrings(attachJSON)
		e.Headers = unmarshalStringMap(headersJSON)
		e.Labels = unmarshalStrings(labelsJSON)
		e.PluginsRun = unmarshalStrings(pluginsRunJSON)
		e.Filtered = filteredInt != 0

		emails[e.UID] = &e
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Prime the row-hash cache so the first SaveEmails after restart is a no-op
	// when nothing changed.
	primed := make(map[uint32][32]byte, len(emails))
	for uid, e := range emails {
		primed[uid] = hashEmailRow(e)
	}
	b.mu.Lock()
	if b.emailHashes[accountName] == nil {
		b.emailHashes[accountName] = make(map[string]map[uint32][32]byte)
	}
	b.emailHashes[accountName][mailboxName] = primed
	b.mu.Unlock()

	return emails, nil
}

func (b *SQLiteBackend) SaveMailboxes(accountName string, mailboxes map[string]*Mailbox) error {
	// Compute fresh per-row hashes, then diff against the last-saved set to
	// produce minimal upserts and deletes.
	fresh := make(map[string][32]byte, len(mailboxes))
	for name, m := range mailboxes {
		fresh[name] = hashMailboxRow(m)
	}

	b.mu.Lock()
	prev, ok := b.mailboxHashes[accountName]
	if !ok {
		prev = nil
	}
	var toUpsert []string
	var toDelete []string
	for name, h := range fresh {
		if p, exists := prev[name]; !exists || p != h {
			toUpsert = append(toUpsert, name)
		}
	}
	for name := range prev {
		if _, stillThere := fresh[name]; !stillThere {
			toDelete = append(toDelete, name)
		}
	}
	b.mu.Unlock()

	if len(toUpsert) == 0 && len(toDelete) == 0 {
		return nil
	}

	db, err := b.getDB(accountName)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(toDelete) > 0 {
		// Emails of a deleted mailbox would be orphaned forever otherwise.
		delMbox, err := tx.Prepare("DELETE FROM mailboxes WHERE name = ?")
		if err != nil {
			return err
		}
		delEmails, err := tx.Prepare("DELETE FROM emails WHERE mailbox = ?")
		if err != nil {
			delMbox.Close()
			return err
		}
		for _, name := range toDelete {
			if _, err := delMbox.Exec(name); err != nil {
				delMbox.Close()
				delEmails.Close()
				return err
			}
			if _, err := delEmails.Exec(name); err != nil {
				delMbox.Close()
				delEmails.Close()
				return err
			}
		}
		delMbox.Close()
		delEmails.Close()
	}

	if len(toUpsert) > 0 {
		upStmt, err := tx.Prepare(`INSERT INTO mailboxes (name, messages, unseen)
			VALUES (?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET messages = excluded.messages, unseen = excluded.unseen`)
		if err != nil {
			return err
		}
		for _, name := range toUpsert {
			m := mailboxes[name]
			if _, err := upStmt.Exec(m.Name, m.Messages, m.Unseen); err != nil {
				upStmt.Close()
				return err
			}
		}
		upStmt.Close()
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	b.mu.Lock()
	b.mailboxHashes[accountName] = fresh
	for _, name := range toDelete {
		delete(b.emailHashes[accountName], name)
	}
	b.mu.Unlock()
	return nil
}

func (b *SQLiteBackend) SaveEmails(accountName, mailboxName string, emails map[uint32]*CachedEmail) error {
	// Compute per-row hashes and diff against the last-saved set so we only
	// touch rows that actually changed.
	fresh := make(map[uint32][32]byte, len(emails))
	for uid, e := range emails {
		fresh[uid] = hashEmailRow(e)
	}

	b.mu.Lock()
	var prev map[uint32][32]byte
	if mboxes, ok := b.emailHashes[accountName]; ok {
		prev = mboxes[mailboxName]
	}
	var toUpsert []uint32
	var toDelete []uint32
	for uid, h := range fresh {
		if p, exists := prev[uid]; !exists || p != h {
			toUpsert = append(toUpsert, uid)
		}
	}
	for uid := range prev {
		if _, stillThere := fresh[uid]; !stillThere {
			toDelete = append(toDelete, uid)
		}
	}
	b.mu.Unlock()
	return b.writeEmailRows(accountName, mailboxName, emails, fresh, toUpsert, toDelete)
}

// SaveEmailsWithHint hashes only the hinted rows; deletions are detected by
// comparing key sets, which needs no hashing.
func (b *SQLiteBackend) SaveEmailsWithHint(accountName, mailboxName string, emails map[uint32]*CachedEmail, changedUIDs []uint32) error {
	b.mu.Lock()
	var prev map[uint32][32]byte
	if mboxes, ok := b.emailHashes[accountName]; ok {
		prev = mboxes[mailboxName]
	}
	fresh := make(map[uint32][32]byte, len(prev)+len(changedUIDs))
	for uid, h := range prev {
		fresh[uid] = h
	}
	var toUpsert []uint32
	for _, uid := range changedUIDs {
		e, ok := emails[uid]
		if !ok {
			continue // removed from the cache meanwhile; prev diff deletes it
		}
		h := hashEmailRow(e)
		fresh[uid] = h
		if p, exists := prev[uid]; !exists || p != h {
			toUpsert = append(toUpsert, uid)
		}
	}
	var toDelete []uint32
	for uid := range prev {
		if _, stillThere := emails[uid]; !stillThere {
			toDelete = append(toDelete, uid)
			delete(fresh, uid)
		}
	}
	b.mu.Unlock()
	return b.writeEmailRows(accountName, mailboxName, emails, fresh, toUpsert, toDelete)
}

func (b *SQLiteBackend) writeEmailRows(accountName, mailboxName string, emails map[uint32]*CachedEmail, fresh map[uint32][32]byte, toUpsert, toDelete []uint32) error {

	if len(toUpsert) == 0 && len(toDelete) == 0 {
		return nil
	}

	db, err := b.getDB(accountName)
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(toDelete) > 0 {
		delStmt, err := tx.Prepare("DELETE FROM emails WHERE mailbox = ? AND uid = ?")
		if err != nil {
			return err
		}
		for _, uid := range toDelete {
			if _, err := delStmt.Exec(mailboxName, uid); err != nil {
				delStmt.Close()
				return err
			}
		}
		delStmt.Close()
	}

	if len(toUpsert) == 0 {
		if err := tx.Commit(); err != nil {
			return err
		}
		b.mu.Lock()
		if b.emailHashes[accountName] == nil {
			b.emailHashes[accountName] = make(map[string]map[uint32][32]byte)
		}
		b.emailHashes[accountName][mailboxName] = fresh
		b.mu.Unlock()
		return nil
	}

	stmt, err := tx.Prepare(`INSERT INTO emails (
		uid, mailbox, subject, date, message_id, in_reply_to,
		from_addrs, sender_addrs, reply_to_addrs, to_addrs, cc_addrs, bcc_addrs,
		body, content_type, flags, attachments, headers, labels, plugins_run, filtered
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(uid, mailbox) DO UPDATE SET
		subject = excluded.subject,
		date = excluded.date,
		message_id = excluded.message_id,
		in_reply_to = excluded.in_reply_to,
		from_addrs = excluded.from_addrs,
		sender_addrs = excluded.sender_addrs,
		reply_to_addrs = excluded.reply_to_addrs,
		to_addrs = excluded.to_addrs,
		cc_addrs = excluded.cc_addrs,
		bcc_addrs = excluded.bcc_addrs,
		body = excluded.body,
		content_type = excluded.content_type,
		flags = excluded.flags,
		attachments = excluded.attachments,
		headers = excluded.headers,
		labels = excluded.labels,
		plugins_run = excluded.plugins_run,
		filtered = excluded.filtered`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, uid := range toUpsert {
		e := emails[uid]
		var subject, dateStr, messageID, inReplyTo string
		var fromJSON, senderJSON, replyToJSON, toJSON, ccJSON, bccJSON string

		if e.Envelope != nil {
			subject = e.Envelope.Subject
			dateStr = e.Envelope.Date.Format(time.RFC3339)
			messageID = e.Envelope.MessageID
			inReplyTo = e.Envelope.InReplyTo
			fromJSON = marshalAddresses(e.Envelope.From)
			senderJSON = marshalAddresses(e.Envelope.Sender)
			replyToJSON = marshalAddresses(e.Envelope.ReplyTo)
			toJSON = marshalAddresses(e.Envelope.To)
			ccJSON = marshalAddresses(e.Envelope.Cc)
			bccJSON = marshalAddresses(e.Envelope.Bcc)
		} else {
			fromJSON = "[]"
			senderJSON = "[]"
			replyToJSON = "[]"
			toJSON = "[]"
			ccJSON = "[]"
			bccJSON = "[]"
		}

		filteredInt := 0
		if e.Filtered {
			filteredInt = 1
		}
		if _, err := stmt.Exec(
			e.UID, mailboxName, subject, dateStr, messageID, inReplyTo,
			fromJSON, senderJSON, replyToJSON, toJSON, ccJSON, bccJSON,
			e.Body, e.ContentType,
			marshalStrings(e.Flags), marshalStrings(e.Attachments),
			marshalStringMap(e.Headers), marshalStrings(e.Labels), marshalStrings(e.PluginsRun), filteredInt,
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.emailHashes[accountName] == nil {
		b.emailHashes[accountName] = make(map[string]map[uint32][32]byte)
	}
	b.emailHashes[accountName][mailboxName] = fresh
	b.mu.Unlock()
	return nil
}

// ResetFiltered flips the filtered column for the whole account in one statement.
func (b *SQLiteBackend) ResetFiltered(accountName string) error {
	db, err := b.getDB(accountName)
	if err != nil {
		return err
	}
	_, err = db.Exec("UPDATE emails SET filtered = 0 WHERE filtered <> 0")
	return err
}

func (b *SQLiteBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, db := range b.dbs {
		db.Close()
		delete(b.dbs, name)
	}
	return nil
}

// JSON serialization helpers

func marshalAddresses(addrs []*Address) string {
	if addrs == nil {
		return "[]"
	}
	data, _ := json.Marshal(addrs)
	return string(data)
}

func unmarshalAddresses(s string) []*Address {
	var addrs []*Address
	if err := json.Unmarshal([]byte(s), &addrs); err != nil {
		slog.Warn("sqlite cache: bad address column", "value", s, "error", err)
	}
	return addrs
}

func marshalStrings(ss []string) string {
	if ss == nil {
		return "[]"
	}
	data, _ := json.Marshal(ss)
	return string(data)
}

func unmarshalStrings(s string) []string {
	var ss []string
	if err := json.Unmarshal([]byte(s), &ss); err != nil {
		slog.Warn("sqlite cache: bad string-list column", "value", s, "error", err)
	}
	return ss
}

func marshalStringMap(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	data, _ := json.Marshal(m)
	return string(data)
}

func unmarshalStringMap(s string) map[string]string {
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		slog.Warn("sqlite cache: bad string-map column", "value", s, "error", err)
	}
	return m
}

// hashMailboxRow produces a stable hash of a single mailbox row's persisted fields.
func hashMailboxRow(m *Mailbox) [32]byte {
	row := struct {
		Name     string
		Messages uint32
		Unseen   uint32
	}{m.Name, m.Messages, m.Unseen}
	data, _ := json.Marshal(row)
	return sha256.Sum256(data)
}

// hashEmailRow produces a stable hash of a single email's persisted fields.
// Headers iteration is non-deterministic, so we sort keys for stability.
func hashEmailRow(e *CachedEmail) [32]byte {
	var headerKeys []string
	for k := range e.Headers {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)
	type kv struct{ K, V string }
	headers := make([]kv, 0, len(headerKeys))
	for _, k := range headerKeys {
		headers = append(headers, kv{k, e.Headers[k]})
	}
	row := struct {
		UID         uint32
		Envelope    *Envelope
		Body        string
		ContentType string
		Flags       []string
		Attachments []string
		Headers     []kv
		Labels      []string
		PluginsRun  []string
		Filtered    bool
	}{
		UID:         e.UID,
		Envelope:    e.Envelope,
		Body:        e.Body,
		ContentType: e.ContentType,
		Flags:       e.Flags,
		Attachments: e.Attachments,
		Headers:     headers,
		Labels:      e.Labels,
		PluginsRun:  e.PluginsRun,
		Filtered:    e.Filtered,
	}
	data, _ := json.Marshal(row)
	return sha256.Sum256(data)
}

// Full-text index over subject, body and addresses. Rows are keyed by the
// emails table's rowid so the triggers delete by indexed lookup. The database
// is never VACUUMed, which is what keeps those rowids stable.
var ftsStatements = []string{
	`CREATE VIRTUAL TABLE IF NOT EXISTS emails_fts USING fts5(subject, body, addrs, tokenize='unicode61')`,
	`CREATE TRIGGER IF NOT EXISTS emails_fts_ai AFTER INSERT ON emails BEGIN
		INSERT INTO emails_fts(rowid, subject, body, addrs)
		VALUES (new.rowid, new.subject, new.body, new.from_addrs || ' ' || new.to_addrs || ' ' || new.cc_addrs || ' ' || new.bcc_addrs);
	END`,
	`CREATE TRIGGER IF NOT EXISTS emails_fts_ad AFTER DELETE ON emails BEGIN
		DELETE FROM emails_fts WHERE rowid = old.rowid;
	END`,
	`CREATE TRIGGER IF NOT EXISTS emails_fts_au AFTER UPDATE ON emails BEGIN
		DELETE FROM emails_fts WHERE rowid = old.rowid;
		INSERT INTO emails_fts(rowid, subject, body, addrs)
		VALUES (new.rowid, new.subject, new.body, new.from_addrs || ' ' || new.to_addrs || ' ' || new.cc_addrs || ' ' || new.bcc_addrs);
	END`,
}

// ensureTextIndex creates the FTS table and triggers and rebuilds the index
// from existing rows when it is missing or out of step (first run after an
// upgrade, or a dropped table).
func ensureTextIndex(db *sql.DB) error {
	for _, stmt := range ftsStatements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	var rows, indexed int
	if err := db.QueryRow("SELECT count(*) FROM emails").Scan(&rows); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT count(*) FROM emails_fts").Scan(&indexed); err != nil {
		return err
	}
	if rows == indexed {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM emails_fts"); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO emails_fts(rowid, subject, body, addrs)
		SELECT rowid, subject, body, from_addrs || ' ' || to_addrs || ' ' || cc_addrs || ' ' || bcc_addrs FROM emails`); err != nil {
		return err
	}
	return tx.Commit()
}

// ftsQuery turns bare words and quoted phrases into an FTS5 MATCH expression:
// every term is required and matches as a prefix.
func ftsQuery(words []string) string {
	terms := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}
	return strings.Join(terms, " ")
}

// SearchText returns the UIDs per mailbox whose subject, body or addresses
// contain every word (prefix match, case-insensitive). Only persisted rows are
// indexed; mail changed since the last save is found by the next save.
func (b *SQLiteBackend) SearchText(accountName, mailboxName string, words []string) (map[string]map[uint32]bool, error) {
	out := make(map[string]map[uint32]bool)
	match := ftsQuery(words)
	if match == "" {
		return out, nil
	}
	db, err := b.getDB(accountName)
	if err != nil {
		return nil, err
	}
	query := `SELECT e.uid, e.mailbox FROM emails_fts f JOIN emails e ON e.rowid = f.rowid WHERE emails_fts MATCH ?`
	args := []any{match}
	if mailboxName != "" {
		query += " AND e.mailbox = ?"
		args = append(args, mailboxName)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid uint32
		var mbox string
		if err := rows.Scan(&uid, &mbox); err != nil {
			return nil, err
		}
		if out[mbox] == nil {
			out[mbox] = make(map[uint32]bool)
		}
		out[mbox][uid] = true
	}
	return out, rows.Err()
}

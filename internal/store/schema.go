package store

// migrations holds the schema in order. migrations[i] upgrades the database
// from user_version i to i+1. Never edit a shipped migration; append a new one.
var migrations = []string{
	// v1: mailboxes, messages, bodies.
	`
CREATE TABLE mailboxes (
	account      TEXT    NOT NULL,
	name         TEXT    NOT NULL,
	kind         TEXT    NOT NULL,
	display_name TEXT    NOT NULL,
	uidvalidity  INTEGER NOT NULL,
	highest_uid  INTEGER NOT NULL DEFAULT 0,
	last_sync    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, name)
);

CREATE TABLE messages (
	account       TEXT    NOT NULL,
	mailbox       TEXT    NOT NULL,
	uidvalidity   INTEGER NOT NULL,
	uid           INTEGER NOT NULL,
	message_id    TEXT    NOT NULL DEFAULT '',
	subject       TEXT    NOT NULL DEFAULT '',
	from_json     TEXT    NOT NULL DEFAULT '[]',
	to_json       TEXT    NOT NULL DEFAULT '[]',
	date          INTEGER NOT NULL DEFAULT 0,
	flags         TEXT    NOT NULL DEFAULT '[]',
	in_reply_to   TEXT    NOT NULL DEFAULT '',
	refs          TEXT    NOT NULL DEFAULT '',
	body_state    TEXT    NOT NULL DEFAULT 'none' CHECK (body_state IN ('none', 'cached')),
	body_attempts INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, mailbox, uidvalidity, uid),
	FOREIGN KEY (account, mailbox) REFERENCES mailboxes (account, name) ON DELETE CASCADE
);

CREATE INDEX messages_mailbox_date ON messages (account, mailbox, date DESC, uid DESC);
CREATE INDEX messages_message_id   ON messages (message_id);
CREATE INDEX messages_body_pending ON messages (account, date DESC, uid DESC) WHERE body_state = 'none';

CREATE TABLE bodies (
	account                 TEXT    NOT NULL,
	mailbox                 TEXT    NOT NULL,
	uidvalidity             INTEGER NOT NULL,
	uid                     INTEGER NOT NULL,
	text_gz                 BLOB,
	html_gz                 BLOB,
	attachments_meta_json   TEXT    NOT NULL DEFAULT '[]',
	PRIMARY KEY (account, mailbox, uidvalidity, uid),
	FOREIGN KEY (account, mailbox, uidvalidity, uid)
		REFERENCES messages (account, mailbox, uidvalidity, uid) ON DELETE CASCADE
);
`,
}

// SchemaVersion is the schema version this build writes.
var SchemaVersion = len(migrations)

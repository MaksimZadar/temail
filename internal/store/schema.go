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
	schemaV2,
	schemaV3,
}

// v2: the body queue index follows the cursor order used by PendingBodies.
// Added after v1 was written on an unmerged branch; kept as a migration so
// databases created from v1 upgrade too.
const schemaV2 = `
DROP INDEX messages_body_pending;
CREATE INDEX messages_body_pending ON messages
	(account, date DESC, mailbox DESC, uidvalidity DESC, uid DESC) WHERE body_state = 'none';
`

// v3: one message entity per Proton internal id, with placements per mailbox.
// The cache is disposable, so messages and bodies are dropped and the sync state
// is reset. Every existing cache resyncs once after this migration. Mailboxes
// are kept. Their unseen count starts at 0 and is set by the next reconcile.
const schemaV3 = `
DROP TABLE bodies;
DROP TABLE messages;

CREATE TABLE messages (
	account       TEXT    NOT NULL,
	mid           TEXT    NOT NULL,
	canonical     INTEGER NOT NULL DEFAULT 1 CHECK (canonical IN (0, 1)),
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
	PRIMARY KEY (account, mid)
);

CREATE TABLE placements (
	account     TEXT    NOT NULL,
	mailbox     TEXT    NOT NULL,
	uidvalidity INTEGER NOT NULL,
	uid         INTEGER NOT NULL,
	mid         TEXT    NOT NULL,
	-- date is the message date, copied here so a mailbox page is read in date
	-- order from the placement index alone (see placements_list).
	date        INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, mailbox, uidvalidity, uid),
	FOREIGN KEY (account, mailbox) REFERENCES mailboxes (account, name) ON DELETE CASCADE,
	FOREIGN KEY (account, mid) REFERENCES messages (account, mid)
);

CREATE INDEX placements_mid ON placements (account, mid);
CREATE INDEX placements_list ON placements (account, mailbox, uidvalidity, date DESC, uid DESC);
CREATE INDEX messages_date ON messages (account, date DESC, mid DESC);
CREATE INDEX messages_body_pending ON messages (account, date DESC, mid DESC)
	WHERE body_state = 'none';

CREATE TABLE bodies (
	account               TEXT NOT NULL,
	mid                   TEXT NOT NULL,
	text_gz               BLOB,
	html_gz               BLOB,
	attachments_meta_json TEXT NOT NULL DEFAULT '[]',
	PRIMARY KEY (account, mid),
	FOREIGN KEY (account, mid) REFERENCES messages (account, mid) ON DELETE CASCADE
);

ALTER TABLE mailboxes ADD COLUMN unseen INTEGER NOT NULL DEFAULT 0;
-- uidnext is the server's UIDNEXT at the last successful sync of the mailbox.
ALTER TABLE mailboxes ADD COLUMN uidnext INTEGER NOT NULL DEFAULT 0;
-- placements_count and bodies_count are maintained by the triggers below, so
-- Counts never scans placements.
ALTER TABLE mailboxes ADD COLUMN placements_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mailboxes ADD COLUMN bodies_count INTEGER NOT NULL DEFAULT 0;
UPDATE mailboxes SET highest_uid = 0, last_sync = 0;

-- The counters on mailboxes follow the base tables. Each trigger adjusts only
-- the mailboxes a row touches, through the placement index. A message is "seen"
-- when its JSON flags array contains the string \\Seen (JSON writes that as two
-- backslashes, which the literals below match).
CREATE TRIGGER placements_ai AFTER INSERT ON placements BEGIN
	UPDATE mailboxes SET
		placements_count = placements_count + 1,
		bodies_count = bodies_count + (SELECT COUNT(*) FROM messages m
			WHERE m.account = NEW.account AND m.mid = NEW.mid AND m.body_state = 'cached'),
		unseen = unseen + (SELECT COUNT(*) FROM messages m
			WHERE m.account = NEW.account AND m.mid = NEW.mid AND instr(m.flags, '\\Seen') = 0)
	WHERE account = NEW.account AND name = NEW.mailbox;
END;

CREATE TRIGGER placements_ad AFTER DELETE ON placements BEGIN
	UPDATE mailboxes SET
		placements_count = placements_count - 1,
		bodies_count = bodies_count - (SELECT COUNT(*) FROM messages m
			WHERE m.account = OLD.account AND m.mid = OLD.mid AND m.body_state = 'cached'),
		unseen = unseen - (SELECT COUNT(*) FROM messages m
			WHERE m.account = OLD.account AND m.mid = OLD.mid AND instr(m.flags, '\\Seen') = 0)
	WHERE account = OLD.account AND name = OLD.mailbox;
END;

-- A placement moved to another message: the placement count is unchanged, but
-- the body and unseen counts follow the message that now holds it.
CREATE TRIGGER placements_au AFTER UPDATE OF mid ON placements
WHEN OLD.mid <> NEW.mid BEGIN
	UPDATE mailboxes SET
		bodies_count = bodies_count
			+ (SELECT COUNT(*) FROM messages m WHERE m.account = NEW.account AND m.mid = NEW.mid AND m.body_state = 'cached')
			- (SELECT COUNT(*) FROM messages m WHERE m.account = OLD.account AND m.mid = OLD.mid AND m.body_state = 'cached'),
		unseen = unseen
			+ (SELECT COUNT(*) FROM messages m WHERE m.account = NEW.account AND m.mid = NEW.mid AND instr(m.flags, '\\Seen') = 0)
			- (SELECT COUNT(*) FROM messages m WHERE m.account = OLD.account AND m.mid = OLD.mid AND instr(m.flags, '\\Seen') = 0)
	WHERE account = NEW.account AND name = NEW.mailbox;
END;

CREATE TRIGGER messages_body_au AFTER UPDATE OF body_state ON messages
WHEN OLD.body_state <> NEW.body_state BEGIN
	UPDATE mailboxes SET bodies_count = bodies_count + CASE WHEN NEW.body_state = 'cached' THEN 1 ELSE -1 END
	WHERE account = NEW.account AND name IN
		(SELECT p.mailbox FROM placements p WHERE p.account = NEW.account AND p.mid = NEW.mid);
END;

CREATE TRIGGER messages_flags_au AFTER UPDATE OF flags ON messages
WHEN (instr(OLD.flags, '\\Seen') = 0) <> (instr(NEW.flags, '\\Seen') = 0) BEGIN
	UPDATE mailboxes SET unseen = unseen + CASE WHEN instr(NEW.flags, '\\Seen') = 0 THEN 1 ELSE -1 END
	WHERE account = NEW.account AND name IN
		(SELECT p.mailbox FROM placements p WHERE p.account = NEW.account AND p.mid = NEW.mid);
END;
`

// SchemaVersion is the schema version this build writes.
var SchemaVersion = len(migrations)

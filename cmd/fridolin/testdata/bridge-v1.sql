-- bridge.db as this version makes it, from TestGenerateFixtures (go test -run TestGenerateFixtures -generate).
-- Synthetic data only. Load it with loadSQL.
CREATE TABLE whatsmeow_version (version INTEGER, compat INTEGER);
INSERT INTO "whatsmeow_version" ("version", "compat") VALUES (14, 8);
CREATE TABLE whatsmeow_device (
	jid TEXT PRIMARY KEY,
	lid TEXT,

	facebook_uuid uuid,

	registration_id BIGINT NOT NULL CHECK ( registration_id >= 0 AND registration_id < 4294967296 ),

	noise_key    bytea NOT NULL CHECK ( length(noise_key) = 32 ),
	identity_key bytea NOT NULL CHECK ( length(identity_key) = 32 ),

	signed_pre_key     bytea   NOT NULL CHECK ( length(signed_pre_key) = 32 ),
	signed_pre_key_id  INTEGER NOT NULL CHECK ( signed_pre_key_id >= 0 AND signed_pre_key_id < 16777216 ),
	signed_pre_key_sig bytea   NOT NULL CHECK ( length(signed_pre_key_sig) = 64 ),

	adv_key             bytea NOT NULL,
	adv_details         bytea NOT NULL,
	adv_account_sig     bytea NOT NULL CHECK ( length(adv_account_sig) = 64 ),
	adv_account_sig_key bytea NOT NULL CHECK ( length(adv_account_sig_key) = 32 ),
	adv_device_sig      bytea NOT NULL CHECK ( length(adv_device_sig) = 64 ),

	platform      TEXT NOT NULL DEFAULT '',
	business_name TEXT NOT NULL DEFAULT '',
	push_name     TEXT NOT NULL DEFAULT '',

	lid_migration_ts BIGINT NOT NULL DEFAULT 0
);
INSERT INTO "whatsmeow_device" ("jid", "lid", "facebook_uuid", "registration_id", "noise_key", "identity_key", "signed_pre_key", "signed_pre_key_id", "signed_pre_key_sig", "adv_key", "adv_details", "adv_account_sig", "adv_account_sig_key", "adv_device_sig", "platform", "business_name", "push_name", "lid_migration_ts") VALUES ('15551234567:1@s.whatsapp.net', NULL, NULL, 689425724, X'B8C4AEB80381B9D4963D66CBC9C52CFB48CB98AD888A44979091A4A1EC02FD57', X'808EBBE4085E3CE75928E83921157EC3FB219C0DA0513A23D10A6750F11CC64D', X'40E19D0B318173EC0EAF5D22AA64490D4CB1CD13DE99A484BAF6EC8606101E6E', 1, X'53D8A71C58B331D352A389E47D2ADB99FD593B2FE0961A1A0B739495EE2549302B8065D6BDEE90AC57DC1BA623A9156087E4B5132F6EB93205AF7CC4DCB8190A', X'9983E891C800FB79A6788EB9000AFB368ECCDF8D65B77042C9A13760AD8A8471', X'E3548B85AA4C721DD82AD75DC063703C1749416FDF83FA6C630996F8FD6B6B7E6A9A7036982A00D5', X'B913C6235B99970B9BCCC22C9685291233361702F9D8FFCA67185DA7E663498BDFA4875846EA26810D501CB61A26D28B72DECA78EB4D0C4CC6EBA6CEF6115DFD', X'572EF9E9D7AD3706E262A6894DDC437E4B61DA857F53311EBE92592A6200BD83', X'9F975C968FC96C18B96D5542A8E0113B706E20A5BCF49AC6D8EF240E472A2F044BF5A874E9A6A11963C2C96B376A83E29B90835D156B78FC12A818D572B5DB24', 'smba', '', 'You', 0);
CREATE TABLE whatsmeow_identity_keys (
	our_jid  TEXT,
	their_id TEXT,
	identity bytea NOT NULL CHECK ( length(identity) = 32 ),

	PRIMARY KEY (our_jid, their_id),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_pre_keys (
	jid      TEXT,
	key_id   INTEGER          CHECK ( key_id >= 0 AND key_id < 16777216 ),
	key      bytea   NOT NULL CHECK ( length(key) = 32 ),
	uploaded BOOLEAN NOT NULL,

	PRIMARY KEY (jid, key_id),
	FOREIGN KEY (jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_sessions (
	our_jid  TEXT,
	their_id TEXT,
	session  bytea,

	PRIMARY KEY (our_jid, their_id),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_sender_keys (
	our_jid    TEXT,
	chat_id    TEXT,
	sender_id  TEXT,
	sender_key bytea NOT NULL,

	PRIMARY KEY (our_jid, chat_id, sender_id),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_app_state_sync_keys (
	jid         TEXT,
	key_id      bytea,
	key_data    bytea  NOT NULL,
	timestamp   BIGINT NOT NULL,
	fingerprint bytea  NOT NULL,

	PRIMARY KEY (jid, key_id),
	FOREIGN KEY (jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_app_state_version (
	jid     TEXT,
	name    TEXT,
	version BIGINT NOT NULL,
	hash    bytea  NOT NULL CHECK ( length(hash) = 128 ),

	PRIMARY KEY (jid, name),
	FOREIGN KEY (jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_app_state_mutation_macs (
	jid       TEXT,
	name      TEXT,
	version   BIGINT,
	index_mac bytea          CHECK ( length(index_mac) = 32 ),
	value_mac bytea NOT NULL CHECK ( length(value_mac) = 32 ),

	PRIMARY KEY (jid, name, version, index_mac),
	FOREIGN KEY (jid, name) REFERENCES whatsmeow_app_state_version(jid, name) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_contacts (
	our_jid        TEXT,
	their_jid      TEXT,
	first_name     TEXT,
	full_name      TEXT,
	push_name      TEXT,
	business_name  TEXT,
	redacted_phone TEXT,

	PRIMARY KEY (our_jid, their_jid),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_chat_settings (
	our_jid       TEXT,
	chat_jid      TEXT,
	muted_until   BIGINT  NOT NULL DEFAULT 0,
	pinned        BOOLEAN NOT NULL DEFAULT false,
	archived      BOOLEAN NOT NULL DEFAULT false,

	PRIMARY KEY (our_jid, chat_jid),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_message_secrets (
	our_jid    TEXT,
	chat_jid   TEXT,
	sender_jid TEXT,
	message_id TEXT,
	key        bytea NOT NULL,

	PRIMARY KEY (our_jid, chat_jid, sender_jid, message_id),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_privacy_tokens (
	our_jid          TEXT,
	their_jid        TEXT,
	token            bytea  NOT NULL,
	timestamp        BIGINT NOT NULL,
	sender_timestamp BIGINT,
	PRIMARY KEY (our_jid, their_jid)
);
CREATE TABLE whatsmeow_nct_salt (
	our_jid TEXT PRIMARY KEY,
	salt    bytea NOT NULL,
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_lid_map (
	lid TEXT PRIMARY KEY,
	pn  TEXT UNIQUE NOT NULL
);
CREATE TABLE whatsmeow_event_buffer (
	our_jid          TEXT   NOT NULL,
	ciphertext_hash  bytea  NOT NULL CHECK ( length(ciphertext_hash) = 32 ),
	plaintext        bytea,
	server_timestamp BIGINT NOT NULL,
	insert_timestamp BIGINT NOT NULL,
	PRIMARY KEY (our_jid, ciphertext_hash),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE whatsmeow_retry_buffer (
	our_jid    TEXT   NOT NULL,
	chat_jid   TEXT   NOT NULL,
	message_id TEXT   NOT NULL,
	format     TEXT   NOT NULL,
	plaintext  bytea  NOT NULL,
	timestamp  BIGINT NOT NULL,

	PRIMARY KEY (our_jid, chat_jid, message_id),
	FOREIGN KEY (our_jid) REFERENCES whatsmeow_device(jid) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE TABLE sessions (
			jid        TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		);
INSERT INTO "sessions" ("jid", "session_id", "updated_at") VALUES ('15551234567@s.whatsapp.net', '00000000-0000-4000-8000-000000000001', '2026-01-02 09:00:00+00:00');
CREATE TABLE process_state (
			jid        TEXT PRIMARY KEY,
			pid        INTEGER NOT NULL,
			updated_at DATETIME NOT NULL
		);
INSERT INTO "process_state" ("jid", "pid", "updated_at") VALUES ('15551234567@s.whatsapp.net', 4242, '2026-01-02 09:00:00+00:00');
CREATE TABLE polls (
			msg_id     TEXT PRIMARY KEY,
			chat_jid   TEXT NOT NULL,
			question   TEXT NOT NULL,
			options    TEXT NOT NULL, -- JSON array of option name strings
			created_at DATETIME NOT NULL
		);
INSERT INTO "polls" ("msg_id", "chat_jid", "question", "options", "created_at") VALUES ('3EB0F1A2B3C4D5E6F7A8', '120363000000000000@g.us', 'Lunch on Friday?', '["Pizza","Sushi","Salad"]', '2026-10-18 01:37:03.740606+00:00');
CREATE TABLE poll_votes (
			poll_msg_id      TEXT NOT NULL,
			voter_jid        TEXT NOT NULL,
			selected_options TEXT NOT NULL, -- JSON array of chosen option name strings
			voted_at         DATETIME NOT NULL,
			PRIMARY KEY (poll_msg_id, voter_jid)
		);
INSERT INTO "poll_votes" ("poll_msg_id", "voter_jid", "selected_options", "voted_at") VALUES ('3EB0F1A2B3C4D5E6F7A8', '15551234567@s.whatsapp.net', '["Pizza","Sushi"]', '2026-10-18 01:37:03.74116+00:00');
INSERT INTO "poll_votes" ("poll_msg_id", "voter_jid", "selected_options", "voted_at") VALUES ('3EB0F1A2B3C4D5E6F7A8', '15559876543@s.whatsapp.net', '["Sushi"]', '2026-10-18 01:37:03.740899+00:00');
CREATE TABLE ignored_chats (
			jid        TEXT PRIMARY KEY,
			kind       TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			sender     TEXT NOT NULL DEFAULT '',
			push_name  TEXT NOT NULL DEFAULT '',
			count      INTEGER NOT NULL DEFAULT 0,
			first_seen DATETIME NOT NULL,
			last_seen  DATETIME NOT NULL,
			dismissed  INTEGER NOT NULL DEFAULT 0
		);
INSERT INTO "ignored_chats" ("jid", "kind", "name", "sender", "push_name", "count", "first_seen", "last_seen", "dismissed") VALUES ('120363000000000001@g.us', 'group', 'Book club', '15559876543', 'Sam', 1, '2026-10-18 01:37:03.741411+00:00', '2026-10-18 01:37:03.741411+00:00', 0);
INSERT INTO "ignored_chats" ("jid", "kind", "name", "sender", "push_name", "count", "first_seen", "last_seen", "dismissed") VALUES ('120363000000000002@g.us', 'group', 'Old group', '15559876543', '', 1, '2026-10-18 01:37:03.741963+00:00', '2026-10-18 01:37:03.741963+00:00', 1);
INSERT INTO "ignored_chats" ("jid", "kind", "name", "sender", "push_name", "count", "first_seen", "last_seen", "dismissed") VALUES ('15559876543@s.whatsapp.net', 'dm', '', '15559876543', 'Sam', 1, '2026-10-18 01:37:03.741687+00:00', '2026-10-18 01:37:03.741687+00:00', 0);
CREATE INDEX idx_whatsmeow_privacy_tokens_our_jid_timestamp
ON whatsmeow_privacy_tokens (our_jid, timestamp);
CREATE INDEX whatsmeow_retry_buffer_timestamp_idx ON whatsmeow_retry_buffer (our_jid, timestamp);
PRAGMA user_version = 0;

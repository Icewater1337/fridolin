package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"fridolin/internal/fsx"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

// The fixtures in testdata are files as this version reads and writes
// them, with synthetic values only: config-v1.json has every setting,
// config-legacy.json the keys older versions wrote, brain-v1.json every
// brain.json field, history-v1.jsonl a brain's history, and bridge-v1.sql
// the database. Later versions must keep reading them.

// generate rewrites the fixtures that are made by code rather than by hand:
// bridge-v1.sql. It is committed, never regenerated in passing: once the
// schema changes, today's version of the database cannot be made again.
var generate = flag.Bool("generate", false, "regenerate testdata/bridge-v1.sql from today's store code")

// fixture reads a file of the package's testdata.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pkgDir, "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fixtureHome is a temp folder with HOME at <root>/home, as the config
// fixtures, whose paths start with ~, expect it.
func fixtureHome(t *testing.T) (root, home string) {
	t.Helper()
	root = fsx.Canonical(t.TempDir())
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return root, home
}

// TestGoldenConfigFixtures loads each config fixture and pins the
// configuration the bridge runs with, what it logs about the file, and what
// the next dashboard save writes back.
func TestGoldenConfigFixtures(t *testing.T) {
	for _, name := range []string{"config-v1", "config-legacy"} {
		t.Run(name, func(t *testing.T) {
			root, _ := fixtureHome(t)
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, fixture(t, name+".json"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := newScrubber(root)
			logs := captureLog(t)
			cs, err := OpenConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			eff, err := json.Marshal(cs.Get())
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, "config/"+name+"-effective.golden", s.json(t, eff))
			assertGolden(t, "config/"+name+"-log.golden", []byte(s.text(strings.Join(logs(), "\n"))+"\n"))

			if _, err := cs.Update(cs.ETag(), func(*Config) error { return nil }); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, "config/"+name+"-saved.golden", []byte(s.text(string(saved))))
		})
	}
}

// TestConfigV1HasEverySetting keeps config-v1.json complete: every JSON key
// of Config, and of each of its sections, is set in it, so a key that is
// added, renamed or dropped changes its golden files. version is the one
// key it lacks: it is a version 1 file, from before the key, and what a
// save makes of it (config-v1-saved.golden) has it.
func TestConfigV1HasEverySetting(t *testing.T) {
	for _, key := range unsetKeys(t, reflect.TypeFor[Config](), fixture(t, "config-v1.json")) {
		if key != "version" {
			t.Errorf("config-v1.json has no %q", key)
		}
	}
}

// unsetKeys lists the JSON keys of struct type typ that data, a JSON
// object, does not set: those of typ itself, and one level down those of
// each field that holds a struct, a pointer to one or a list of them (set
// in any element of the list). A type that writes its own JSON is taken as
// a value.
func unsetKeys(t *testing.T, typ reflect.Type, data []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	var missing []string
	for key, ft := range jsonFields(typ) {
		raw, ok := obj[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		sub := structOf(ft)
		if sub == nil {
			continue
		}
		var objs []map[string]json.RawMessage
		var one map[string]json.RawMessage
		if err := json.Unmarshal(raw, &one); err == nil {
			objs = append(objs, one)
		} else if err := json.Unmarshal(raw, &objs); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		for subKey := range jsonFields(sub) {
			if !slices.ContainsFunc(objs, func(o map[string]json.RawMessage) bool { _, ok := o[subKey]; return ok }) {
				missing = append(missing, key+"."+subKey)
			}
		}
	}
	slices.Sort(missing)
	return missing
}

// jsonFields returns the keys that encoding/json writes for struct type
// typ, each with the type of its value.
func jsonFields(typ reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			maps.Copy(out, jsonFields(f.Type))
		case !f.IsExported():
		case name == "":
			out[f.Name] = f.Type
		default:
			out[name] = f.Type
		}
	}
	return out
}

// structOf returns the struct type that a value of typ is or holds, behind
// pointers and in lists, or nil when there is none or a type on the way
// writes its own JSON.
func structOf(typ reflect.Type) reflect.Type {
	marshaler := reflect.TypeFor[json.Marshaler]()
	for {
		if typ.Implements(marshaler) || reflect.PointerTo(typ).Implements(marshaler) {
			return nil
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice:
			typ = typ.Elem()
		case reflect.Struct:
			return typ
		default:
			return nil
		}
	}
}

func TestUnsetKeys(t *testing.T) {
	// The check itself: a key missing at the top or one level down shows.
	type section struct {
		A string `json:"a"`
		B string `json:"b,omitempty"`
	}
	type config struct {
		Plain   string    `json:"plain,omitempty"`
		Skipped string    `json:"-"`
		Section section   `json:"section"`
		Pointer *section  `json:"pointer,omitempty"`
		List    []section `json:"list,omitempty"`
		Own     TriggerList
	}
	full := `{"plain": "x", "section": {"a": "", "b": ""}, "pointer": {"a": "", "b": ""}, "list": [{"a": ""}, {"b": ""}], "Own": ["x"]}`
	tests := []struct {
		name, data string
		want       []string
	}{
		{"every key", full, nil},
		{"a key at the top", strings.Replace(full, `"plain": "x", `, "", 1), []string{"plain"}},
		{"a key of a section", strings.Replace(full, `"section": {"a": "", "b": ""}`, `"section": {"a": ""}`, 1), []string{"section.b"}},
		{"a key behind a pointer", strings.Replace(full, `"pointer": {"a": "", "b": ""}`, `"pointer": {"b": ""}`, 1), []string{"pointer.a"}},
		{"a key in no element of a list", strings.Replace(full, `{"b": ""}]`, `{"a": ""}]`, 1), []string{"list.b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsetKeys(t, reflect.TypeFor[config](), []byte(tc.data)); !slices.Equal(got, tc.want) {
				t.Errorf("missing = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBrainV1Fixture: brain-v1.json, with every field, reads back and is
// written again byte for byte, as Add brain and every manifest edit write it.
func TestBrainV1Fixture(t *testing.T) {
	data := fixture(t, "brain-v1.json")
	m, err := decodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := encodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("brain-v1.json written again (-fixture +written):\n%s", lineDiff(string(data), string(out)))
	}
	// Every field is set, so a new one shows up here.
	for _, key := range unsetKeys(t, reflect.TypeFor[BrainManifest](), data) {
		t.Errorf("brain-v1.json has no %q", key)
	}
}

// TestHistoryV1Fixture: every line of history-v1.jsonl reads back and is
// written again byte for byte, as appendHistory writes lines, and the
// lines between them have every type and set every field.
func TestHistoryV1Fixture(t *testing.T) {
	sc := bufio.NewScanner(bytes.NewReader(fixture(t, "history-v1.jsonl")))
	kinds := map[string]bool{}
	keys := map[string]json.RawMessage{}
	for sc.Scan() {
		var e historyEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("%s: %v", sc.Bytes(), err)
		}
		kinds[e.Type] = true
		if err := json.Unmarshal(sc.Bytes(), &keys); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, sc.Bytes()) {
			t.Errorf("line written again as\n%s\nwant\n%s", out, sc.Bytes())
		}
	}
	if !kinds[historyIn] || !kinds[historyOut] || !kinds[historyEvent] || !kinds[historyReset] || len(kinds) != 4 {
		t.Errorf("history-v1.jsonl has the types %v, want %q, %q, %q and %q", kinds, historyIn, historyOut, historyEvent, historyReset)
	}
	all, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range unsetKeys(t, reflect.TypeFor[historyEntry](), all) {
		t.Errorf("no line of history-v1.jsonl sets %q", key)
	}
}

// The synthetic contents of bridge-v1.sql.
const (
	fixtureDeviceJID = "15551234567:1@s.whatsapp.net"
	fixturePollID    = "3EB0F1A2B3C4D5E6F7A8"
	fixtureVoter     = "15559876543@s.whatsapp.net"
)

// TestGenerateFixtures writes testdata/bridge-v1.sql with -generate: a
// database made by today's store code, with a paired device (random keys,
// a placeholder number), a poll and its votes, ignored chats (one
// dismissed), and rows in the sessions and process_state tables that only
// older versions wrote. It is a SQL dump rather than the database file: a
// reviewer can read it, and the personal-data check can scan it.
func TestGenerateFixtures(t *testing.T) {
	if !*generate {
		t.Skip("run with -generate to rewrite testdata/bridge-v1.sql")
	}
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	s := openTestStore(t, dbPath)
	ctx := context.Background()

	dev := s.Container.NewDevice()
	jid, err := types.ParseJID(fixtureDeviceJID)
	if err != nil {
		t.Fatal(err)
	}
	dev.ID = &jid
	dev.Platform, dev.PushName = "smba", "You"
	random := func(n int) []byte {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return b
	}
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: random(40), AccountSignature: random(64), AccountSignatureKey: random(32), DeviceSignature: random(64)}
	if err := s.Container.PutDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}

	if err := s.SavePoll(fixturePollID, testGroup, "main", "Lunch on Friday?", []string{"Pizza", "Sushi", "Salad"}); err != nil {
		t.Fatal(err)
	}
	for voter, picks := range map[string][]string{fixtureVoter: {"Sushi"}, "15551234567@s.whatsapp.net": {"Pizza", "Sushi"}} {
		if err := s.SavePollVote(fixturePollID, voter, picks); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordIgnored("group", "120363000000000001@g.us", "Book club", "15559876543", "Sam"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIgnored("dm", "15559876543@s.whatsapp.net", "", "15559876543", "Sam"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIgnored("group", "120363000000000002@g.us", "Old group", "15559876543", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissIgnored("120363000000000002@g.us", false); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO sessions (jid, session_id, updated_at) VALUES ('15551234567@s.whatsapp.net', '00000000-0000-4000-8000-000000000001', '2026-01-02 09:00:00+00:00')`,
		`INSERT INTO process_state (jid, pid, updated_at) VALUES ('15551234567@s.whatsapp.net', 4242, '2026-01-02 09:00:00+00:00')`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	dump, err := dumpSQL(s.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "testdata", "bridge-v1.sql"), dump, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dumpSQL writes db as SQL that makes it again: the tables, their rows
// (each value as SQLite's quote() writes it, so the stored types survive),
// then the indexes, triggers and views, and the user_version.
func dumpSQL(db *sql.DB) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("-- bridge.db as this version makes it, from TestGenerateFixtures (go test -run TestGenerateFixtures -generate).\n")
	out.WriteString("-- Synthetic data only. Load it with loadSQL.\n")
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	type object struct{ typ, name, sql string }
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.typ, &o.name, &o.sql); err != nil {
			rows.Close()
			return nil, err
		}
		objects = append(objects, o)
	}
	rows.Close()
	for _, o := range objects {
		if o.typ != "table" {
			continue
		}
		fmt.Fprintf(&out, "%s;\n", o.sql)
		cols, err := columns(db, o.name)
		if err != nil {
			return nil, err
		}
		quoted := make([]string, len(cols))
		order := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = "quote(" + quoteIdent(c) + ")"
			order[i] = quoteIdent(c)
		}
		vals, err := db.Query(`SELECT ` + strings.Join(quoted, ", ") + ` FROM ` + quoteIdent(o.name) + ` ORDER BY ` + strings.Join(order, ", "))
		if err != nil {
			return nil, err
		}
		for vals.Next() {
			row := make([]string, len(cols))
			ptrs := make([]any, len(cols))
			for i := range row {
				ptrs[i] = &row[i]
			}
			if err := vals.Scan(ptrs...); err != nil {
				vals.Close()
				return nil, err
			}
			fmt.Fprintf(&out, "INSERT INTO %s (%s) VALUES (%s);\n", quoteIdent(o.name), strings.Join(order, ", "), strings.Join(row, ", "))
		}
		vals.Close()
	}
	for _, o := range objects {
		if o.typ != "table" {
			fmt.Fprintf(&out, "%s;\n", o.sql)
		}
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	fmt.Fprintf(&out, "PRAGMA user_version = %d;\n", version)
	return out.Bytes(), nil
}

// columns lists the columns of table, in order.
func columns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// loadSQL makes the database at dbPath from a dump of dumpSQL.
func loadSQL(t testing.TB, dbPath string, dump []byte) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("loading the dump: %v", err)
	}
}

// TestBridgeV1Fixture opens bridge-v1.sql, made into a database, with
// today's store: the device still pairs the bridge, and the polls, votes and
// ignored chats read back.
func TestBridgeV1Fixture(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	loadSQL(t, dbPath, fixture(t, "bridge-v1.sql"))
	s := openTestStore(t, dbPath)

	dev, err := s.Container.GetFirstDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dev.ID == nil || dev.ID.String() != fixtureDeviceJID || dev.PushName != "You" || dev.IdentityKey == nil || dev.Account == nil {
		t.Errorf("device = %v %q, keys %v, account %v; want the paired %s", dev.ID, dev.PushName, dev.IdentityKey != nil, dev.Account != nil, fixtureDeviceJID)
	}

	// The dump is from before polls recorded the brain that sent them, so
	// the poll reads back with none.
	p, err := s.SentPoll(context.Background(), fixturePollID)
	if err != nil || p.Chat != testGroup || p.Brain != "" || p.Question != "Lunch on Friday?" || !slices.Equal(p.Options, []string{"Pizza", "Sushi", "Salad"}) {
		t.Errorf("poll = %+v, %v", p, err)
	}
	var votes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poll_votes WHERE poll_msg_id = ?`, fixturePollID).Scan(&votes); err != nil || votes != 2 {
		t.Errorf("votes = %d, %v; want 2", votes, err)
	}
	var picks string
	if err := s.db.QueryRow(`SELECT selected_options FROM poll_votes WHERE voter_jid = ?`, fixtureVoter).Scan(&picks); err != nil || picks != `["Sushi"]` {
		t.Errorf("%s voted %s, %v", fixtureVoter, picks, err)
	}

	ignored, err := s.ListIgnored()
	if err != nil {
		t.Fatal(err)
	}
	var jids []string
	for _, c := range ignored {
		jids = append(jids, c.Kind+" "+c.JID+" "+c.Name)
	}
	slices.Sort(jids)
	if want := []string{"dm 15559876543@s.whatsapp.net ", "group 120363000000000001@g.us Book club"}; !slices.Equal(jids, want) {
		t.Errorf("ignored chats = %q, want %q (the dismissed one left out)", jids, want)
	}

	// Kept for a rollback to an older bridge, which reads them.
	for _, table := range []string{"sessions", "process_state"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s has %d rows, %v; want 1", table, n, err)
		}
	}
}

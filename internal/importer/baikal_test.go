package importer_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/importer"
	"github.com/bitflipp/galdav/internal/store"
)

// Minimal tables with the column names of a Baikal database.
const fixtureSchema = `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, digesta1 TEXT);
CREATE TABLE principals (id INTEGER PRIMARY KEY, uri TEXT, email TEXT, displayname TEXT);
CREATE TABLE calendars (id INTEGER PRIMARY KEY, synctoken INTEGER, components TEXT);
CREATE TABLE calendarinstances (id INTEGER PRIMARY KEY, calendarid INTEGER, principaluri TEXT, access INTEGER,
  displayname TEXT, uri TEXT, description TEXT, calendarorder INTEGER, calendarcolor TEXT, timezone TEXT);
CREATE TABLE calendarobjects (id INTEGER PRIMARY KEY, calendardata BLOB, uri TEXT, calendarid INTEGER,
  lastmodified INTEGER, etag TEXT, size INTEGER, componenttype TEXT, firstoccurence INTEGER, lastoccurence INTEGER, uid TEXT);
CREATE TABLE addressbooks (id INTEGER PRIMARY KEY, principaluri TEXT, displayname TEXT, uri TEXT, description TEXT, synctoken INTEGER);
CREATE TABLE cards (id INTEGER PRIMARY KEY, addressbookid INTEGER, carddata BLOB, uri TEXT, lastmodified INTEGER, etag TEXT, size INTEGER);
`

const (
	ev = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260301T100000Z\r\nDTEND:20260301T110000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	vc = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c1\r\nFN:Bob\r\nN:;Bob;;;\r\nEND:VCARD\r\n"
)

func fixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "baikal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		fixtureSchema,
		`INSERT INTO users (username, digesta1) VALUES ('alice', '` + auth.HA1("alice", "BaikalDAV", "secret") + `')`,
		`INSERT INTO principals (uri, email, displayname) VALUES ('principals/alice', 'a@example.com', 'Alice A')`,
		`INSERT INTO calendars (id, components) VALUES (1, 'VEVENT'), (2, 'VEVENT,VTODO')`,
		`INSERT INTO calendarinstances (calendarid, principaluri, access, displayname, uri, calendarorder, calendarcolor)
		 VALUES (1, 'principals/alice', 1, 'Default', 'default', 2, '#00ff00'),
		        (2, 'principals/bob', 2, 'Shared', 'shared-copy', 0, '')`, // shared instance: not imported
		`INSERT INTO addressbooks (id, principaluri, displayname, uri) VALUES (1, 'principals/alice', 'Contacts', 'default')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`INSERT INTO calendarobjects (calendardata, uri, calendarid, etag, componenttype, uid) VALUES (?, 'e1.ics', 1, 'abc123', 'VEVENT', 'e1')`, []byte(ev))
	db.Exec(`INSERT INTO calendarobjects (calendardata, uri, calendarid, etag, componenttype, uid) VALUES (?, 'broken.ics', 1, '"q"', 'VEVENT', 'x')`, []byte("not ical"))
	db.Exec(`INSERT INTO cards (addressbookid, carddata, uri, etag) VALUES (1, ?, 'c1.vcf', 'def456')`, []byte(vc))
	return db
}

func TestImportBaikal(t *testing.T) {
	src := fixture(t)
	dst, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	dry, err := importer.ImportBaikal(src, dst, importer.Options{DryRun: true})
	if err != nil || dry.Users != 1 || dry.Events != 2 || dry.Cards != 1 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if _, err := dst.UserByName("alice"); err == nil {
		t.Fatal("dry run wrote a user")
	}

	for run := 0; run < 2; run++ { // second run checks idempotency
		res, err := importer.ImportBaikal(src, dst, importer.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Calendars != 1 || res.Events != 2 || res.AddressBooks != 1 || res.Cards != 1 {
			t.Fatalf("run %d: %+v", run, res)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "broken.ics") {
			t.Fatalf("warnings: %v", res.Warnings)
		}
	}

	u, err := dst.UserByName("alice")
	if err != nil || u.DisplayName != "Alice A" || u.Email != "a@example.com" {
		t.Fatalf("user: %+v %v", u, err)
	}
	a := &auth.Authenticator{Store: dst, Realm: "BaikalDAV"}
	if _, ok := a.Verify("alice", "secret"); !ok {
		t.Fatal("imported digest hash does not verify the old password")
	}
	cal, err := dst.GetCollection(u.ID, store.KindCalendar, "default")
	if err != nil || cal.DisplayName != "Default" || cal.Color != "#00ff00" || cal.SortOrder != 2 || cal.Components != "VEVENT" {
		t.Fatalf("calendar: %+v %v", cal, err)
	}
	objs, _ := dst.ListObjects(cal.ID)
	if len(objs) != 2 {
		t.Fatalf("objects: %+v", objs)
	}
	o, err := dst.GetObject(cal.ID, "e1.ics")
	if err != nil || o.ETag != "abc123" || o.UID != "e1" || !o.FirstOcc.Valid || string(o.Data) != ev {
		t.Fatalf("event: %+v %v", o, err)
	}
	if b, _ := dst.GetObject(cal.ID, "broken.ics"); b.ETag != "q" || b.FirstOcc.Valid {
		t.Fatalf("broken: %+v", b)
	}
	if _, err := dst.GetCollection(u.ID, store.KindCalendar, "shared-copy"); err == nil {
		t.Fatal("shared instance imported")
	}
	book, err := dst.GetCollection(u.ID, store.KindAddressBook, "default")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := dst.GetObject(book.ID, "c1.vcf"); err != nil || c.UID != "c1" {
		t.Fatalf("card: %+v %v", c, err)
	}
}

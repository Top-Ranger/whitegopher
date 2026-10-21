package importer_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/importer"
	"github.com/bitflipp/galdav/internal/store"
	"github.com/bitflipp/galdav/internal/storetest"
)

// Minimal tables with the column names of a Baikal SQLite database.
const sqliteSchema = `
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

// mysqlSchema mimics Baikal's MySQL layout, including its VARBINARY columns.
var mysqlSchema = []string{
	`CREATE TABLE users (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, username VARBINARY(50), digesta1 VARBINARY(32))`,
	`CREATE TABLE principals (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, uri VARBINARY(200), email VARBINARY(80), displayname VARCHAR(80))`,
	`CREATE TABLE calendars (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, synctoken INTEGER UNSIGNED NOT NULL DEFAULT 1, components VARBINARY(21))`,
	`CREATE TABLE calendarinstances (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, calendarid INTEGER UNSIGNED NOT NULL, principaluri VARBINARY(100),
	   access TINYINT(1) NOT NULL DEFAULT 1, displayname VARCHAR(100), uri VARBINARY(200), description TEXT, calendarorder INT UNSIGNED NOT NULL DEFAULT 0,
	   calendarcolor VARBINARY(10), timezone TEXT)`,
	`CREATE TABLE calendarobjects (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, calendardata MEDIUMBLOB, uri VARBINARY(200), calendarid INTEGER UNSIGNED NOT NULL,
	   lastmodified INT UNSIGNED, etag VARBINARY(32), size INT UNSIGNED NOT NULL DEFAULT 0, componenttype VARBINARY(8), firstoccurence BIGINT UNSIGNED, lastoccurence BIGINT UNSIGNED, uid VARBINARY(200))`,
	`CREATE TABLE addressbooks (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, principaluri VARBINARY(255), displayname VARCHAR(255), uri VARBINARY(200), description TEXT, synctoken INTEGER UNSIGNED NOT NULL DEFAULT 1)`,
	`CREATE TABLE cards (id INTEGER UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, addressbookid INTEGER UNSIGNED NOT NULL, carddata MEDIUMBLOB, uri VARBINARY(200),
	   lastmodified INT UNSIGNED, etag VARBINARY(32), size INT UNSIGNED NOT NULL DEFAULT 0)`,
}

func sqliteFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "baikal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	return populate(t, db)
}

func mysqlFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", storetest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, ddl := range mysqlSchema {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return populate(t, db)
}

func populate(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	stmts := []string{
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
	objects := []struct {
		q    string
		data string
	}{
		{`INSERT INTO calendarobjects (calendardata, uri, calendarid, etag, componenttype, uid) VALUES (?, 'e1.ics', 1, 'abc123', 'VEVENT', 'e1')`, ev},
		{`INSERT INTO calendarobjects (calendardata, uri, calendarid, etag, componenttype, uid) VALUES (?, 'broken.ics', 1, '"q"', 'VEVENT', 'x')`, "not ical"},
		{`INSERT INTO cards (addressbookid, carddata, uri, etag) VALUES (1, ?, 'c1.vcf', 'def456')`, vc},
	}
	for _, o := range objects {
		if _, err := db.Exec(o.q, []byte(o.data)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestImportBaikalSQLite(t *testing.T) { testImport(t, sqliteFixture(t)) }

// Runs only when GALDAV_TEST_MYSQL_DSN is set.
func TestImportBaikalMySQL(t *testing.T) { testImport(t, mysqlFixture(t)) }

func testImport(t *testing.T, src *sql.DB) {
	dst := storetest.New(t)

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

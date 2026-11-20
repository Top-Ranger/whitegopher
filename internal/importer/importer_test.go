package importer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Top-Ranger/whitegopher/internal/auth"
	"github.com/Top-Ranger/whitegopher/internal/importer"
	"github.com/Top-Ranger/whitegopher/internal/store"
	"github.com/Top-Ranger/whitegopher/internal/storetest"
)

const (
	ev = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260301T100000Z\r\nDTEND:20260301T110000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	vc = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c1\r\nFN:Bob\r\nN:;Bob;;;\r\nEND:VCARD\r\n"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "whitegopher-import.json", `{"format":"whitegopher-import","version":1,"generator":"test"}`)
	write(t, root, "users/alice/user.json", `{"display_name":"Alice A","email":"a@example.com","digest_ha1":"`+auth.HA1("alice", "realm", "secret")+`"}`)
	c := "users/alice/calendars/default/"
	write(t, root, c+"collection.json", `{"display_name":"Default","color":"#00ff00","sort_order":2,"components":"VEVENT","etags":{"e1.ics":"\"abc123\""}}`)
	write(t, root, c+"objects/e1.ics", ev)
	write(t, root, c+"objects/broken.ics", "not ical")
	write(t, root, c+"objects/a%20b.ics", strings.Replace(ev, "UID:e1", "UID:e2", 1))
	write(t, root, c+"objects/latin1.ics", "\xe9")
	write(t, root, "users/alice/calendars/inbox/collection.json", `{}`)
	write(t, root, "users/alice/addressbooks/default/collection.json", `{"display_name":"Contacts"}`)
	write(t, root, "users/alice/addressbooks/default/objects/c1.vcf", vc)
	return root
}

func TestImport(t *testing.T) {
	dir := fixture(t)
	dst := storetest.New(t)

	dry, err := importer.Import(dir, dst, importer.Options{DryRun: true})
	if err != nil || dry.Users != 1 || dry.Events != 3 || dry.Cards != 1 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if _, err := dst.UserByName("alice"); err == nil {
		t.Fatal("dry run wrote a user")
	}

	for run := 0; run < 2; run++ { // second run checks idempotency
		res, err := importer.Import(dir, dst, importer.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Calendars != 1 || res.Events != 3 || res.AddressBooks != 1 || res.Cards != 1 {
			t.Fatalf("run %d: %+v", run, res)
		}
		if len(res.Warnings) != 3 {
			t.Fatalf("warnings: %v", res.Warnings)
		}
	}

	u, err := dst.UserByName("alice")
	if err != nil || u.DisplayName != "Alice A" || u.Email != "a@example.com" {
		t.Fatalf("user: %+v %v", u, err)
	}
	a := &auth.Authenticator{Store: dst, Realm: "realm"}
	if _, ok := a.Verify("alice", "secret"); !ok {
		t.Fatal("imported digest hash does not verify")
	}
	cal, err := dst.GetCollection(u.ID, store.KindCalendar, "default")
	if err != nil || cal.DisplayName != "Default" || cal.Color != "#00ff00" || cal.SortOrder != 2 || cal.Components != "VEVENT" {
		t.Fatalf("calendar: %+v %v", cal, err)
	}
	o, err := dst.GetObject(cal.ID, "e1.ics")
	if err != nil || o.ETag != "abc123" || o.UID != "e1" || !o.FirstOcc.Valid || string(o.Data) != ev {
		t.Fatalf("event: %+v %v", o, err)
	}
	if _, err := dst.GetObject(cal.ID, "a b.ics"); err != nil {
		t.Fatalf("percent-encoded name: %v", err)
	}
	if b, err := dst.GetObject(cal.ID, "broken.ics"); err != nil || b.FirstOcc.Valid {
		t.Fatalf("broken: %+v %v", b, err)
	}
	if _, err := dst.GetObject(cal.ID, "latin1.ics"); err == nil {
		t.Fatal("non-UTF-8 object imported")
	}
	if _, err := dst.GetCollection(u.ID, store.KindCalendar, "inbox"); err == nil {
		t.Fatal("reserved collection imported")
	}
	book, err := dst.GetCollection(u.ID, store.KindAddressBook, "default")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := dst.GetObject(book.ID, "c1.vcf"); err != nil || c.UID != "c1" {
		t.Fatalf("card: %+v %v", c, err)
	}
}

func TestImportRejectsBadTree(t *testing.T) {
	dst := storetest.New(t)
	for name, manifest := range map[string]string{
		"wrong version": `{"format":"whitegopher-import","version":2}`,
		"wrong format":  `{"format":"other","version":1}`,
		"unknown field": `{"format":"whitegopher-import","version":1,"bogus":1}`,
	} {
		root := t.TempDir()
		write(t, root, "whitegopher-import.json", manifest)
		if _, err := importer.Import(root, dst, importer.Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := importer.Import(t.TempDir(), dst, importer.Options{}); err == nil {
		t.Error("missing manifest accepted")
	}
}

// Package importer copies data out of a Baikal (sabre/dav schema) SQLite or
// MySQL database into a galdav store. It reads the database as plain data and
// shares no code with Baikal or sabre/dav.
package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/bitflipp/galdav/internal/index"
	"github.com/bitflipp/galdav/internal/store"
)

// Result summarises an import run.
type Result struct {
	Users, Calendars, AddressBooks, Events, Cards int
	// Warnings lists objects imported without full indexing or skipped.
	Warnings []string
}

func (r *Result) warnf(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

// Options controls an import.
type Options struct {
	// DryRun reads and validates everything but writes nothing.
	DryRun bool
}

type importer struct {
	src *sql.DB
	dst *store.Store
	opt Options
	res Result
	// users maps principal URI ("principals/alice") to the galdav user id.
	users map[string]int64
}

// ImportBaikal imports users, calendars, address books and their objects
// from the Baikal database src (SQLite or MySQL). It is idempotent: existing users and
// collections are reused and objects are overwritten by URI.
func ImportBaikal(src *sql.DB, dst *store.Store, opt Options) (Result, error) {
	im := &importer{src: src, dst: dst, opt: opt, users: map[string]int64{}}
	if err := im.importUsers(); err != nil {
		return im.res, fmt.Errorf("users: %w", err)
	}
	if err := im.importCalendars(); err != nil {
		return im.res, fmt.Errorf("calendars: %w", err)
	}
	if err := im.importAddressBooks(); err != nil {
		return im.res, fmt.Errorf("address books: %w", err)
	}
	return im.res, nil
}

// hasTable probes for a table in a database-neutral way. Table names are
// fixed identifiers from this package, never user input.
func (im *importer) hasTable(name string) bool {
	rows, err := im.src.Query(`SELECT 1 FROM ` + name + ` WHERE 1 = 0`)
	if err != nil {
		return false
	}
	rows.Close()
	return true
}

func (im *importer) importUsers() error {
	rows, err := im.src.Query(`SELECT username, digesta1 FROM users ORDER BY username`)
	if err != nil {
		return err
	}
	type row struct{ name, ha1 string }
	var list []row
	for rows.Next() {
		var r row
		var ha1 sql.NullString
		if err := rows.Scan(&r.name, &ha1); err != nil {
			rows.Close()
			return err
		}
		r.ha1 = ha1.String
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	display := map[string]string{}
	email := map[string]string{}
	if im.hasTable("principals") {
		rs, err := im.src.Query(`SELECT uri, displayname, email FROM principals`)
		if err != nil {
			return err
		}
		for rs.Next() {
			var uri string
			var dn, em sql.NullString
			if err := rs.Scan(&uri, &dn, &em); err != nil {
				rs.Close()
				return err
			}
			display[uri], email[uri] = dn.String, em.String
		}
		rs.Close()
	}

	for _, r := range list {
		uri := "principals/" + r.name
		if im.opt.DryRun {
			im.users[uri] = -1
			im.res.Users++
			continue
		}
		u, err := im.dst.UserByName(r.name)
		switch {
		case err == nil:
			im.users[uri] = u.ID
			continue
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		id, err := im.dst.CreateUser(store.User{Username: r.name, DisplayName: display[uri], Email: email[uri], DigestHA1: r.ha1})
		if err != nil {
			return err
		}
		im.users[uri] = id
		im.res.Users++
	}
	return nil
}

// ensureCollection returns the id of the (owner, kind, slug) collection,
// creating it if needed.
func (im *importer) ensureCollection(c store.Collection) (int64, error) {
	if im.opt.DryRun {
		return -1, nil
	}
	existing, err := im.dst.GetCollection(c.OwnerID, c.Kind, c.Slug)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	return im.dst.CreateCollection(c)
}

func (im *importer) importCalendars() error {
	// Newer schemas split calendars (shared data) from calendarinstances
	// (per-principal properties); older ones keep everything in calendars.
	var q string
	if im.hasTable("calendarinstances") {
		q = `SELECT ci.calendarid, ci.principaluri, ci.uri, ci.displayname, ci.description, ci.calendarorder,
			ci.calendarcolor, ci.timezone, c.components
			FROM calendarinstances ci JOIN calendars c ON c.id = ci.calendarid
			WHERE ci.access = 1`
	} else {
		q = `SELECT id, principaluri, uri, displayname, description, calendarorder, calendarcolor, timezone, components FROM calendars`
	}
	rows, err := im.src.Query(q)
	if err != nil {
		return err
	}
	type cal struct {
		srcID                             int64
		principal, uri                    string
		name, desc, color, tz, components sql.NullString
		order                             sql.NullInt64
	}
	var cals []cal
	for rows.Next() {
		var c cal
		if err := rows.Scan(&c.srcID, &c.principal, &c.uri, &c.name, &c.desc, &c.order, &c.color, &c.tz, &c.components); err != nil {
			rows.Close()
			return err
		}
		cals = append(cals, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range cals {
		owner, ok := im.users[c.principal]
		if !ok {
			im.res.warnf("calendar %q: unknown owner %q, skipped", c.uri, c.principal)
			continue
		}
		comps := c.components.String
		if comps == "" {
			comps = "VEVENT,VTODO"
		}
		coll := store.Collection{
			OwnerID: owner, Kind: store.KindCalendar, Slug: c.uri, DisplayName: c.name.String,
			Description: c.desc.String, Color: c.color.String, SortOrder: int(c.order.Int64),
			Components: comps, Timezone: c.tz.String,
		}
		id, err := im.ensureCollection(coll)
		if err != nil {
			return fmt.Errorf("calendar %q: %w", c.uri, err)
		}
		im.res.Calendars++
		if err := im.copyObjects(id, c.principal+"/"+c.uri,
			`SELECT uri, calendardata, etag, componenttype FROM calendarobjects WHERE calendarid = ?`, c.srcID,
			func(uri string, data []byte, ct string) (index.Parsed, string) {
				p, bad := index.Calendar(data, strings.Split(comps, ","))
				if bad != "" {
					p = index.Parsed{UID: uri, Component: ct}
				}
				return p, bad
			}, &im.res.Events); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) importAddressBooks() error {
	rows, err := im.src.Query(`SELECT id, principaluri, uri, displayname, description FROM addressbooks`)
	if err != nil {
		return err
	}
	type book struct {
		srcID          int64
		principal, uri string
		name, desc     sql.NullString
	}
	var books []book
	for rows.Next() {
		var b book
		if err := rows.Scan(&b.srcID, &b.principal, &b.uri, &b.name, &b.desc); err != nil {
			rows.Close()
			return err
		}
		books = append(books, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, b := range books {
		owner, ok := im.users[b.principal]
		if !ok {
			im.res.warnf("address book %q: unknown owner %q, skipped", b.uri, b.principal)
			continue
		}
		id, err := im.ensureCollection(store.Collection{
			OwnerID: owner, Kind: store.KindAddressBook, Slug: b.uri, DisplayName: b.name.String, Description: b.desc.String,
		})
		if err != nil {
			return fmt.Errorf("address book %q: %w", b.uri, err)
		}
		im.res.AddressBooks++
		if err := im.copyObjects(id, b.principal+"/"+b.uri,
			`SELECT uri, carddata, etag, 'VCARD' FROM cards WHERE addressbookid = ?`, b.srcID,
			func(uri string, data []byte, _ string) (index.Parsed, string) {
				p, bad := index.Card(data, uri)
				if bad != "" {
					p = index.Parsed{UID: uri, Component: "VCARD"}
				}
				return p, bad
			}, &im.res.Cards); err != nil {
			return err
		}
	}
	return nil
}

// copyObjects streams the objects selected by query (uri, data, etag,
// component) into collection dst. Objects failing validation are still
// imported verbatim but without index data, with a warning.
func (im *importer) copyObjects(dst int64, label, query string, srcID int64,
	idx func(uri string, data []byte, component string) (index.Parsed, string), counter *int) error {
	rows, err := im.src.Query(query, srcID)
	if err != nil {
		return err
	}
	type obj struct {
		uri, etag, comp string
		data            []byte
	}
	var objs []obj
	for rows.Next() {
		var o obj
		var etag, comp sql.NullString
		var data any
		if err := rows.Scan(&o.uri, &data, &etag, &comp); err != nil {
			rows.Close()
			return err
		}
		switch d := data.(type) {
		case []byte:
			o.data = d
		case string:
			o.data = []byte(d)
		}
		o.etag, o.comp = strings.Trim(etag.String, `"`), comp.String
		objs = append(objs, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range objs {
		p, bad := idx(o.uri, o.data, o.comp)
		if bad != "" {
			im.res.warnf("%s/%s: failed validation (%s); imported without index data", label, o.uri, bad)
		}
		*counter++
		if im.opt.DryRun {
			continue
		}
		if o.etag == "" {
			o.etag = index.ETag(o.data)
		}
		_, err := im.dst.PutObject(store.Object{
			CollectionID: dst, URI: o.uri, UID: p.UID, ETag: o.etag, Data: o.data,
			Component: p.Component, FirstOcc: p.First, LastOcc: p.Last,
		}, store.PutCond{AllowDupUID: true})
		if err != nil {
			return fmt.Errorf("%s/%s: %w", label, o.uri, err)
		}
	}
	return nil
}

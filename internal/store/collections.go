package store

import (
	"database/sql"
	"errors"
	"strings"
)

const (
	KindCalendar    = "calendar"
	KindAddressBook = "addressbook"
)

var ErrExists = errors.New("store: already exists")

type Collection struct {
	ID          int64
	OwnerID     int64
	Kind        string
	Slug        string
	DisplayName string
	Description string
	Color       string
	SortOrder   int
	Components  string // comma-separated, calendars only
	Timezone    string // VTIMEZONE text, calendars only
	// Special is "" for ordinary collections and "inbox" for a user's
	// scheduling inbox.
	Special string
	// Transparent calendars are left out of free-busy.
	Transparent bool
}

const SpecialInbox = "inbox"

const collCols = `id, owner_id, kind, slug, display_name, description, color, sort_order, components, timezone, special, transparent`

func scanCollection(sc interface{ Scan(...any) error }) (Collection, error) {
	var c Collection
	err := sc.Scan(&c.ID, &c.OwnerID, &c.Kind, &c.Slug, &c.DisplayName, &c.Description, &c.Color, &c.SortOrder, &c.Components, &c.Timezone, &c.Special, &c.Transparent)
	return c, err
}

// CreateCollection inserts c and returns its id. Empty Components defaults
// to the schema default.
func (s *Store) CreateCollection(c Collection) (int64, error) {
	if c.Components == "" {
		c.Components = "VEVENT,VTODO"
	}
	res, err := s.DB.Exec(
		`INSERT INTO collections (owner_id, kind, slug, display_name, description, color, sort_order, components, timezone, special, transparent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.OwnerID, c.Kind, c.Slug, c.DisplayName, c.Description, c.Color, c.SortOrder, c.Components, c.Timezone, c.Special, c.Transparent)
	if isUnique(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetCollection(ownerID int64, kind, slug string) (Collection, error) {
	c, err := scanCollection(s.DB.QueryRow(
		`SELECT `+collCols+` FROM collections WHERE owner_id = ? AND kind = ? AND slug = ?`, ownerID, kind, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ListCollections(ownerID int64, kind string) ([]Collection, error) {
	rows, err := s.DB.Query(
		`SELECT `+collCols+` FROM collections WHERE owner_id = ? AND kind = ? AND special = '' ORDER BY sort_order, slug`, ownerID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCollections excludes special collections such as the inbox.

// UpdateCollection writes the mutable properties of c.
func (s *Store) UpdateCollection(c Collection) error {
	_, err := s.DB.Exec(
		`UPDATE collections SET display_name = ?, description = ?, color = ?, sort_order = ?, timezone = ?, transparent = ? WHERE id = ?`,
		c.DisplayName, c.Description, c.Color, c.SortOrder, c.Timezone, c.Transparent, c.ID)
	return err
}

func (s *Store) DeleteCollection(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM collections WHERE id = ?`, id)
	return err
}

// SyncSeq returns the id of the newest change in the collection, or 0.
// It serves as both ctag and sync-token.
func (s *Store) SyncSeq(collectionID int64) (int64, error) {
	var n sql.NullInt64
	err := s.DB.QueryRow(`SELECT max(id) FROM changes WHERE collection_id = ?`, collectionID).Scan(&n)
	return n.Int64, err
}

func (s *Store) GetCollectionByID(id int64) (Collection, error) {
	c, err := scanCollection(s.DB.QueryRow(`SELECT `+collCols+` FROM collections WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ObjectCount(collectionID int64) (n int, err error) {
	err = s.DB.QueryRow(`SELECT count(*) FROM objects WHERE collection_id = ?`, collectionID).Scan(&n)
	return
}

// Inbox returns the user's scheduling inbox, creating it on first use.
func (s *Store) Inbox(ownerID int64) (Collection, error) {
	c, err := s.GetCollection(ownerID, KindCalendar, SpecialInbox)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return c, err
	}
	c = Collection{OwnerID: ownerID, Kind: KindCalendar, Slug: SpecialInbox, DisplayName: "Inbox",
		Components: "VEVENT,VTODO,VJOURNAL,VFREEBUSY", Special: SpecialInbox}
	c.ID, err = s.CreateCollection(c)
	if errors.Is(err, ErrExists) { // lost a race, or a plain calendar holds the name
		if c, err = s.GetCollection(ownerID, KindCalendar, SpecialInbox); err == nil && c.Special != SpecialInbox {
			err = errors.New("store: a calendar named \"inbox\" blocks the scheduling inbox")
		}
	}
	return c, err
}

// DefaultCalendar picks the calendar that receives invitations for u: the
// user's choice if it still exists, else the first calendar accepting
// components of the given type.
func (s *Store) DefaultCalendar(u User, component string) (Collection, error) {
	if u.DefaultCalendar != "" {
		if c, err := s.GetCollection(u.ID, KindCalendar, u.DefaultCalendar); err == nil && c.Special == "" && hasComponent(c, component) {
			return c, nil
		}
	}
	cs, err := s.ListCollections(u.ID, KindCalendar)
	if err != nil {
		return Collection{}, err
	}
	for _, c := range cs {
		if hasComponent(c, component) {
			return c, nil
		}
	}
	return Collection{}, ErrNotFound
}

func hasComponent(c Collection, component string) bool {
	for _, n := range strings.Split(c.Components, ",") {
		if n == component {
			return true
		}
	}
	return false
}

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
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

const collCols = `id, owner_id, kind, slug, display_name, description, color, sort_order, components, timezone`

func scanCollection(sc interface{ Scan(...any) error }) (Collection, error) {
	var c Collection
	err := sc.Scan(&c.ID, &c.OwnerID, &c.Kind, &c.Slug, &c.DisplayName, &c.Description, &c.Color, &c.SortOrder, &c.Components, &c.Timezone)
	return c, err
}

// CreateCollection inserts c and returns its id. Empty Components defaults
// to the schema default.
func (s *Store) CreateCollection(c Collection) (int64, error) {
	if c.Components == "" {
		c.Components = "VEVENT,VTODO"
	}
	res, err := s.DB.Exec(
		`INSERT INTO collections (owner_id, kind, slug, display_name, description, color, sort_order, components, timezone)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.OwnerID, c.Kind, c.Slug, c.DisplayName, c.Description, c.Color, c.SortOrder, c.Components, c.Timezone)
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
		`SELECT `+collCols+` FROM collections WHERE owner_id = ? AND kind = ? ORDER BY sort_order, slug`, ownerID, kind)
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

// UpdateCollection writes the mutable properties of c.
func (s *Store) UpdateCollection(c Collection) error {
	_, err := s.DB.Exec(
		`UPDATE collections SET display_name = ?, description = ?, color = ?, sort_order = ?, timezone = ? WHERE id = ?`,
		c.DisplayName, c.Description, c.Color, c.SortOrder, c.Timezone, c.ID)
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

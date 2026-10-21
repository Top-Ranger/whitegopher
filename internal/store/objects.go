package store

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrPrecondition = errors.New("store: precondition failed")

// UIDConflictError reports another object in the collection with the same UID.
type UIDConflictError struct{ URI string }

func (e *UIDConflictError) Error() string { return "store: uid already used by " + e.URI }

type Object struct {
	ID           int64
	CollectionID int64
	URI          string
	UID          string
	ETag         string // unquoted
	Data         []byte // nil when loaded by ListObjects
	Size         int64
	Component    string
	// FirstOcc and LastOcc are unix seconds bounding the object's
	// occurrences. NULL means unknown or unbounded: queries must treat such
	// objects as candidates.
	FirstOcc sql.NullInt64
	LastOcc  sql.NullInt64
}

// PutCond expresses the conditional-request headers of a PUT.
type PutCond struct {
	// IfNoneMatchAny requires that the object does not exist.
	IfNoneMatchAny bool
	// IfMatch, when non-nil, requires the current ETag to satisfy it.
	IfMatch func(etag string) bool
	// AllowDupUID skips the UID-uniqueness check (used when importing).
	AllowDupUID bool
}

// PutObject creates or replaces o and logs a change. created reports
// whether the object is new.
func (s *Store) PutObject(o Object, cond PutCond) (created bool, err error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Serialise writers per collection so the UID check and the insert are
	// atomic.
	var id int64
	if err := tx.QueryRow(`SELECT id FROM collections WHERE id = ?`+forUpdate, o.CollectionID).Scan(&id); err != nil {
		return false, err
	}
	var oldETag string
	err = tx.QueryRow(`SELECT etag FROM objects WHERE collection_id = ? AND uri = ?`+forUpdate, o.CollectionID, o.URI).Scan(&oldETag)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if cond.IfNoneMatchAny && exists {
		return false, ErrPrecondition
	}
	if cond.IfMatch != nil && (!exists || !cond.IfMatch(oldETag)) {
		return false, ErrPrecondition
	}

	if !cond.AllowDupUID && o.UID != "" {
		var other string
		err = tx.QueryRow(`SELECT uri FROM objects WHERE collection_id = ? AND uid = ? AND uri != ? LIMIT 1`,
			o.CollectionID, o.UID, o.URI).Scan(&other)
		if err == nil {
			return false, &UIDConflictError{URI: other}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}

	_, err = tx.Exec(
		`INSERT INTO objects (collection_id, uri, uid, etag, data, component, first_occ, last_occ)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   uid = VALUES(uid), etag = VALUES(etag), data = VALUES(data), component = VALUES(component),
		   first_occ = VALUES(first_occ), last_occ = VALUES(last_occ), updated_at = CURRENT_TIMESTAMP`,
		o.CollectionID, o.URI, o.UID, o.ETag, o.Data, o.Component, o.FirstOcc, o.LastOcc)
	if err != nil {
		return false, fmt.Errorf("put object: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO changes (collection_id, uri, deleted) VALUES (?, ?, 0)`, o.CollectionID, o.URI); err != nil {
		return false, err
	}
	return !exists, tx.Commit()
}

const objMeta = `id, collection_id, uri, uid, etag, length(data), component, first_occ, last_occ`

func scanObject(sc interface{ Scan(...any) error }, withData bool) (Object, error) {
	var o Object
	dest := []any{&o.ID, &o.CollectionID, &o.URI, &o.UID, &o.ETag, &o.Size, &o.Component, &o.FirstOcc, &o.LastOcc}
	if withData {
		dest = append(dest, &o.Data)
	}
	return o, sc.Scan(dest...)
}

func (s *Store) GetObject(collectionID int64, uri string) (Object, error) {
	o, err := scanObject(s.DB.QueryRow(
		`SELECT `+objMeta+`, data FROM objects WHERE collection_id = ? AND uri = ?`, collectionID, uri), true)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// ListObjects returns the collection's objects without their data.
func (s *Store) ListObjects(collectionID int64) ([]Object, error) {
	rows, err := s.DB.Query(`SELECT `+objMeta+` FROM objects WHERE collection_id = ? ORDER BY uri`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o, err := scanObject(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteObject removes an object and logs the deletion. ifMatch, when
// non-nil, must accept the current ETag.
func (s *Store) DeleteObject(collectionID int64, uri string, ifMatch func(etag string) bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var etag string
	err = tx.QueryRow(`SELECT etag FROM objects WHERE collection_id = ? AND uri = ?`+forUpdate, collectionID, uri).Scan(&etag)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ifMatch != nil && !ifMatch(etag) {
		return ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM objects WHERE collection_id = ? AND uri = ?`, collectionID, uri); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO changes (collection_id, uri, deleted) VALUES (?, ?, 1)`, collectionID, uri); err != nil {
		return err
	}
	return tx.Commit()
}

// QueryObjects returns objects with data, optionally narrowed by component
// and an overlap window in unix seconds. Objects with unknown (NULL) bounds
// always pass the window, so callers must still evaluate precisely.
func (s *Store) QueryObjects(collectionID int64, component string, from, to *int64) ([]Object, error) {
	q := `SELECT ` + objMeta + `, data FROM objects WHERE collection_id = ?`
	args := []any{collectionID}
	if component != "" {
		q += ` AND component = ?`
		args = append(args, component)
	}
	if from != nil {
		q += ` AND (last_occ IS NULL OR last_occ >= ?)`
		args = append(args, *from)
	}
	if to != nil {
		q += ` AND (first_occ IS NULL OR first_occ <= ?)`
		args = append(args, *to)
	}
	rows, err := s.DB.Query(q+` ORDER BY uri`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o, err := scanObject(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ChangedURIs returns the URIs touched by changes in (since, upto], oldest
// first. Whether each still exists tells changed from deleted.
func (s *Store) ChangedURIs(collectionID, since, upto int64) ([]string, error) {
	rows, err := s.DB.Query(
		`SELECT uri FROM changes WHERE collection_id = ? AND id > ? AND id <= ? GROUP BY uri ORDER BY max(id)`,
		collectionID, since, upto)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

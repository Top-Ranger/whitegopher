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
	// ScheduleTag is set on scheduling object resources only (RFC 6638).
	ScheduleTag string
	// FirstOcc and LastOcc are unix seconds bounding the object's
	// occurrences. NULL means unknown or unbounded: queries must treat such
	// objects as candidates.
	FirstOcc sql.NullInt64
	LastOcc  sql.NullInt64
	// Modified is the last write time in unix seconds (0 when not loaded).
	Modified int64
}

// PutCond expresses the conditional-request headers of a PUT.
type PutCond struct {
	// IfNoneMatchAny requires that the object does not exist.
	IfNoneMatchAny bool
	// IfMatch, when non-nil, requires the current ETag to satisfy it.
	IfMatch func(etag string) bool
	// AllowDupUID skips the UID-uniqueness check (used when importing).
	AllowDupUID bool
	// IfScheduleTag, when non-nil, requires the current Schedule-Tag to
	// satisfy it (If-Schedule-Tag-Match).
	IfScheduleTag func(tag string) bool
	// KeepScheduleTag leaves an existing object's Schedule-Tag as it is
	// instead of taking o.ScheduleTag.
	KeepScheduleTag bool
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
	var oldETag, oldTag string
	err = tx.QueryRow(`SELECT etag, schedule_tag FROM objects WHERE collection_id = ? AND uri = ?`+forUpdate, o.CollectionID, o.URI).Scan(&oldETag, &oldTag)
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

	if cond.IfScheduleTag != nil && (!exists || !cond.IfScheduleTag(oldTag)) {
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
		`INSERT INTO objects (collection_id, uri, uid, etag, data, component, first_occ, last_occ, schedule_tag)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   uid = VALUES(uid), etag = VALUES(etag), data = VALUES(data), component = VALUES(component),
		   first_occ = VALUES(first_occ), last_occ = VALUES(last_occ),
		   schedule_tag = IF(?, schedule_tag, VALUES(schedule_tag)), updated_at = CURRENT_TIMESTAMP`,
		o.CollectionID, o.URI, o.UID, o.ETag, o.Data, o.Component, o.FirstOcc, o.LastOcc, o.ScheduleTag, cond.KeepScheduleTag)
	if err != nil {
		return false, fmt.Errorf("put object: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO changes (collection_id, uri, deleted) VALUES (?, ?, 0)`, o.CollectionID, o.URI); err != nil {
		return false, err
	}
	return !exists, tx.Commit()
}

const objMeta = `id, collection_id, uri, uid, etag, length(data), component, first_occ, last_occ, schedule_tag, UNIX_TIMESTAMP(updated_at)`

func scanObject(sc interface{ Scan(...any) error }, withData bool) (Object, error) {
	var o Object
	dest := []any{&o.ID, &o.CollectionID, &o.URI, &o.UID, &o.ETag, &o.Size, &o.Component, &o.FirstOcc, &o.LastOcc, &o.ScheduleTag, &o.Modified}
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

// FindByUID returns the object with the given UID in any of the user's
// regular calendars (not the scheduling inbox).
func (s *Store) FindByUID(ownerID int64, uid string) (Collection, Object, error) {
	var collID int64
	var o Object
	err := s.DB.QueryRow(
		`SELECT o.collection_id, o.id, o.collection_id, o.uri, o.uid, o.etag, length(o.data), o.component, o.first_occ, o.last_occ, o.data
		 FROM objects o JOIN collections c ON c.id = o.collection_id
		 WHERE c.owner_id = ? AND c.kind = 'calendar' AND c.special = '' AND o.uid = ?
		 ORDER BY o.id LIMIT 1`, ownerID, uid,
	).Scan(&collID, &o.ID, &o.CollectionID, &o.URI, &o.UID, &o.ETag, &o.Size, &o.Component, &o.FirstOcc, &o.LastOcc, &o.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, o, ErrNotFound
	}
	if err != nil {
		return Collection{}, o, err
	}
	c, err := s.GetCollectionByID(collID)
	return c, o, err
}

// UsageBytes returns the size of all object data in collections owned by
// ownerID.
func (s *Store) UsageBytes(ownerID int64) (n int64, err error) {
	err = s.DB.QueryRow(`SELECT COALESCE(SUM(LENGTH(o.data)), 0) FROM objects o
		JOIN collections c ON c.id = o.collection_id WHERE c.owner_id = ?`, ownerID).Scan(&n)
	return
}

// ObjectSize returns the stored size of an object, or 0 if it does not exist.
func (s *Store) ObjectSize(collectionID int64, uri string) (n int64, err error) {
	err = s.DB.QueryRow(`SELECT COALESCE(LENGTH(data), 0) FROM objects WHERE collection_id = ? AND uri = ?`, collectionID, uri).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return
}

// PruneInbox deletes scheduling-inbox items older than days, logging the
// deletions so syncing clients drop them too. It returns how many went.
func (s *Store) PruneInbox(days int) (int, error) {
	total := 0
	for {
		rows, err := s.DB.Query(`SELECT o.collection_id, o.uri FROM objects o
			JOIN collections c ON c.id = o.collection_id
			WHERE c.special = ? AND o.updated_at < NOW() - INTERVAL ? DAY LIMIT 500`, SpecialInbox, days)
		if err != nil {
			return total, err
		}
		type item struct {
			coll int64
			uri  string
		}
		var batch []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.coll, &it.uri); err != nil {
				rows.Close()
				return total, err
			}
			batch = append(batch, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(batch) == 0 {
			return total, err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return total, err
		}
		for _, it := range batch {
			res, err := tx.Exec(`DELETE FROM objects WHERE collection_id = ? AND uri = ?`, it.coll, it.uri)
			if err != nil {
				tx.Rollback()
				return total, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO changes (collection_id, uri, deleted) VALUES (?, ?, 1)`, it.coll, it.uri); err != nil {
				tx.Rollback()
				return total, err
			}
			total++
		}
		if err := tx.Commit(); err != nil {
			return total, err
		}
	}
}

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Share access levels.
const (
	AccessRead      = "read"
	AccessReadWrite = "readwrite"
)

// Share is a collection mounted into another user's namespace.
type Share struct {
	ID           int64
	CollectionID int64
	UserID       int64
	Kind         string
	Slug         string
	Access       string
	DisplayName  string
	Color        string
	SortOrder    int
	Transparent  bool
}

// Sharee is a share together with the user it is shared with.
type Sharee struct {
	Share
	Username    string
	UserDisplay string
	Email       string
}

func reservedSlug(s string) bool { return s == "inbox" || s == "outbox" }

// SlugTaken reports whether slug is unavailable in the user's namespace for
// the kind: an own collection, a share mount, or a reserved name.
func (s *Store) SlugTaken(userID int64, kind, slug string) (bool, error) {
	if reservedSlug(slug) {
		return true, nil
	}
	var n int
	err := s.DB.QueryRow(
		`SELECT (SELECT count(*) FROM collections WHERE owner_id = ? AND kind = ? AND slug = ?)
		      + (SELECT count(*) FROM collection_shares WHERE user_id = ? AND kind = ? AND slug = ?)`,
		userID, kind, slug, userID, kind, slug).Scan(&n)
	return n > 0, err
}

// FreeSlug returns the first of the candidate bases that is free in the
// user's namespace, else the first base with a numeric suffix.
func (s *Store) FreeSlug(userID int64, kind string, bases ...string) (string, error) {
	for _, b := range bases {
		taken, err := s.SlugTaken(userID, kind, b)
		if err != nil {
			return "", err
		}
		if !taken {
			return b, nil
		}
	}
	for i := 2; i < 100; i++ {
		c := bases[0] + "-" + strconv.Itoa(i)
		taken, err := s.SlugTaken(userID, kind, c)
		if err != nil {
			return "", err
		}
		if !taken {
			return c, nil
		}
	}
	return "", errors.New("store: no free name for the shared collection")
}

// SetShare shares collection c with the user at the given access level,
// creating the mount or changing the access of an existing one.
func (s *Store) SetShare(c Collection, userID int64, access string) (Share, error) {
	if access != AccessRead && access != AccessReadWrite {
		return Share{}, fmt.Errorf("store: invalid access %q", access)
	}
	if c.Special != "" || c.Access != "" {
		return Share{}, errors.New("store: this collection cannot be shared")
	}
	if userID == c.OwnerID {
		return Share{}, errors.New("store: cannot share with the owner")
	}
	res, err := s.DB.Exec(`UPDATE collection_shares SET access = ? WHERE collection_id = ? AND user_id = ?`, access, c.ID, userID)
	if err != nil {
		return Share{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// RowsAffected is 0 both when absent and when unchanged; check which.
		var one int
		err := s.DB.QueryRow(`SELECT 1 FROM collection_shares WHERE collection_id = ? AND user_id = ?`, c.ID, userID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			owner, oerr := s.UserByID(c.OwnerID)
			if oerr != nil {
				return Share{}, oerr
			}
			slug, ferr := s.FreeSlug(userID, c.Kind, c.Slug, owner.Username+"-"+c.Slug)
			if ferr != nil {
				return Share{}, ferr
			}
			if _, err := s.DB.Exec(
				`INSERT INTO collection_shares (collection_id, user_id, kind, slug, access) VALUES (?, ?, ?, ?, ?)`,
				c.ID, userID, c.Kind, slug, access); err != nil {
				return Share{}, err
			}
		} else if err != nil {
			return Share{}, err
		}
	}
	return s.getShare(c.ID, userID)
}

const shareCols = `id, collection_id, user_id, kind, slug, access, display_name, color, sort_order, transparent`

func scanShare(sc interface{ Scan(...any) error }) (Share, error) {
	var sh Share
	err := sc.Scan(&sh.ID, &sh.CollectionID, &sh.UserID, &sh.Kind, &sh.Slug, &sh.Access, &sh.DisplayName, &sh.Color, &sh.SortOrder, &sh.Transparent)
	return sh, err
}

func (s *Store) getShare(collectionID, userID int64) (Share, error) {
	sh, err := scanShare(s.DB.QueryRow(`SELECT `+shareCols+` FROM collection_shares WHERE collection_id = ? AND user_id = ?`, collectionID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return sh, ErrNotFound
	}
	return sh, err
}

// DeleteShare removes a share; it reports ErrNotFound if there was none.
func (s *Store) DeleteShare(collectionID, userID int64) error {
	res, err := s.DB.Exec(`DELETE FROM collection_shares WHERE collection_id = ? AND user_id = ?`, collectionID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Sharees lists who a collection is shared with.
func (s *Store) Sharees(collectionID int64) ([]Sharee, error) {
	rows, err := s.DB.Query(
		`SELECT sh.id, sh.collection_id, sh.user_id, sh.kind, sh.slug, sh.access, sh.display_name, sh.color, sh.sort_order, sh.transparent,
		        u.username, u.display_name, u.email
		 FROM collection_shares sh JOIN users u ON u.id = sh.user_id
		 WHERE sh.collection_id = ? ORDER BY u.username`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sharee
	for rows.Next() {
		var x Sharee
		if err := rows.Scan(&x.ID, &x.CollectionID, &x.UserID, &x.Kind, &x.Slug, &x.Access, &x.DisplayName, &x.Color, &x.SortOrder, &x.Transparent,
			&x.Username, &x.UserDisplay, &x.Email); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// mountCols selects a collection joined with a share and its owner.
const mountSelect = `SELECT c.id, c.owner_id, c.kind, c.slug, c.display_name, c.description, c.color, c.sort_order, c.components, c.timezone, c.special, c.transparent,
	sh.id, sh.slug, sh.access, sh.display_name, sh.color, sh.sort_order, sh.transparent, u.username
	FROM collection_shares sh
	JOIN collections c ON c.id = sh.collection_id
	JOIN users u ON u.id = c.owner_id`

func scanMount(sc interface{ Scan(...any) error }) (Collection, error) {
	var c Collection
	var shSlug, shName, shColor string
	var shOrder int
	var shTransp bool
	err := sc.Scan(&c.ID, &c.OwnerID, &c.Kind, &c.Slug, &c.DisplayName, &c.Description, &c.Color, &c.SortOrder, &c.Components, &c.Timezone, &c.Special, &c.Transparent,
		&c.ShareID, &shSlug, &c.Access, &shName, &shColor, &shOrder, &shTransp, &c.OwnerName)
	if err != nil {
		return c, err
	}
	c.Slug = shSlug
	if shName != "" {
		c.DisplayName = shName
	}
	if shColor != "" {
		c.Color = shColor
	}
	c.SortOrder, c.Transparent = shOrder, shTransp
	return c, nil
}

// ResolveCollection finds a collection in the user's namespace: one they own,
// else one shared with them under that slug.
func (s *Store) ResolveCollection(userID int64, kind, slug string) (Collection, error) {
	c, err := s.GetCollection(userID, kind, slug)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return c, err
	}
	c, err = scanMount(s.DB.QueryRow(mountSelect+` WHERE sh.user_id = ? AND sh.kind = ? AND sh.slug = ?`, userID, kind, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListMounts returns the collections of a kind that are shared with the user.
func (s *Store) ListMounts(userID int64, kind string) ([]Collection, error) {
	rows, err := s.DB.Query(mountSelect+` WHERE sh.user_id = ? AND sh.kind = ? ORDER BY sh.sort_order, sh.slug`, userID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Collection
	for rows.Next() {
		c, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MountByID returns the share mount with the given id if it belongs to the user.
func (s *Store) MountByID(userID, shareID int64) (Collection, error) {
	c, err := scanMount(s.DB.QueryRow(mountSelect+` WHERE sh.user_id = ? AND sh.id = ?`, userID, shareID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// UpdateShareProps stores the sharee's own presentation settings.
func (s *Store) UpdateShareProps(shareID int64, displayName, color string, order int, transparent bool) error {
	_, err := s.DB.Exec(`UPDATE collection_shares SET display_name = ?, color = ?, sort_order = ?, transparent = ? WHERE id = ?`,
		displayName, color, order, transparent, shareID)
	return err
}

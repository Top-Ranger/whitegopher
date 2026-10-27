package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Share access levels.
const (
	AccessRead      = "read"
	AccessReadWrite = "readwrite"
)

// Share states. Only accepted shares appear in the sharee's namespace.
const (
	SharePending  = "pending"
	ShareAccepted = "accepted"
	ShareDeclined = "declined"
	// ShareExpired is an invitation withdrawn because nobody answered it.
	ShareExpired = "expired"
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
	State        string
	// InviteUID identifies the invitation in notifications.
	InviteUID string
}

// Sharee is a share together with the user it is shared with.
type Sharee struct {
	Share
	Username    string
	UserDisplay string
	Email       string
}

func reservedSlug(s string) bool { return s == "inbox" || s == "outbox" || s == SpecialNotification }

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

func newInviteUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// SetShare shares collection c with the user at the given access level,
// creating the mount or changing the access of an existing one. A new share
// (or one the user had declined) waits for acceptance when requireAccept is
// set, else is accepted at once.
func (s *Store) SetShare(c Collection, userID int64, access string, requireAccept bool) (Share, error) {
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
			state := ShareAccepted
			if requireAccept {
				state = SharePending
			}
			if _, err := s.DB.Exec(
				`INSERT INTO collection_shares (collection_id, user_id, kind, slug, access, state, invite_uid) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				c.ID, userID, c.Kind, slug, access, state, newInviteUID()); err != nil {
				return Share{}, err
			}
		} else if err != nil {
			return Share{}, err
		}
	}
	// Inviting somebody who declined, or never answered, asks again.
	state := ShareAccepted
	if requireAccept {
		state = SharePending
	}
	if _, err := s.DB.Exec(`UPDATE collection_shares SET state = ?, state_changed_at = NOW() WHERE collection_id = ? AND user_id = ? AND state IN (?, ?)`,
		state, c.ID, userID, ShareDeclined, ShareExpired); err != nil {
		return Share{}, err
	}
	return s.getShare(c.ID, userID)
}

// SetShareState records the sharee's answer to an invitation.
func (s *Store) SetShareState(shareID, userID int64, state string) error {
	if state != SharePending && state != ShareAccepted && state != ShareDeclined && state != ShareExpired {
		return fmt.Errorf("store: invalid share state %q", state)
	}
	res, err := s.DB.Exec(`UPDATE collection_shares SET state = ?, state_changed_at = NOW() WHERE id = ? AND user_id = ?`, state, shareID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := s.DB.QueryRow(`SELECT 1 FROM collection_shares WHERE id = ? AND user_id = ?`, shareID, userID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
	}
	return nil
}

// ShareByInvite finds the user's share by its invitation uid.
func (s *Store) ShareByInvite(userID int64, uid string) (Share, error) {
	sh, err := scanShare(s.DB.QueryRow(`SELECT `+shareCols+` FROM collection_shares WHERE user_id = ? AND invite_uid = ?`, userID, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return sh, ErrNotFound
	}
	return sh, err
}

// ShareFor returns the share of a collection with a user.
func (s *Store) ShareFor(collectionID, userID int64) (Share, error) {
	return s.getShare(collectionID, userID)
}

// ShareByID returns the user's share with the given id, in any state.
func (s *Store) ShareByID(userID, shareID int64) (Share, error) {
	sh, err := scanShare(s.DB.QueryRow(`SELECT `+shareCols+` FROM collection_shares WHERE user_id = ? AND id = ?`, userID, shareID))
	if errors.Is(err, sql.ErrNoRows) {
		return sh, ErrNotFound
	}
	return sh, err
}

// Invite is a share waiting for the user's answer, with what they need to
// decide.
type Invite struct {
	Share
	Collection   Collection
	OwnerName    string
	OwnerEmail   string
	OwnerDisplay string
}

// PendingInvites lists the shares waiting for the user to accept.
func (s *Store) PendingInvites(userID int64) ([]Invite, error) {
	rows, err := s.DB.Query(`SELECT `+shareColsQ("sh")+`, c.id, c.owner_id, c.kind, c.slug, c.display_name, c.description, c.color, c.sort_order, c.components, c.timezone, c.special, c.transparent,
		u.username, u.display_name, u.email
		FROM collection_shares sh JOIN collections c ON c.id = sh.collection_id JOIN users u ON u.id = c.owner_id
		WHERE sh.user_id = ? AND sh.state = ? ORDER BY sh.id`, userID, SharePending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var iv Invite
		c := &iv.Collection
		if err := rows.Scan(&iv.ID, &iv.CollectionID, &iv.UserID, &iv.Share.Kind, &iv.Share.Slug, &iv.Access, &iv.Share.DisplayName, &iv.Share.Color, &iv.SortOrder, &iv.Transparent, &iv.State, &iv.InviteUID,
			&c.ID, &c.OwnerID, &c.Kind, &c.Slug, &c.DisplayName, &c.Description, &c.Color, &c.SortOrder, &c.Components, &c.Timezone, &c.Special, &c.Transparent,
			&iv.OwnerName, &iv.OwnerDisplay, &iv.OwnerEmail); err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

const shareCols = `id, collection_id, user_id, kind, slug, access, display_name, color, sort_order, transparent, state, invite_uid`

// shareColsQ is shareCols with every column qualified by the table alias.
func shareColsQ(alias string) string {
	cols := strings.Split(shareCols, ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

func scanShare(sc interface{ Scan(...any) error }) (Share, error) {
	var sh Share
	err := sc.Scan(&sh.ID, &sh.CollectionID, &sh.UserID, &sh.Kind, &sh.Slug, &sh.Access, &sh.DisplayName, &sh.Color, &sh.SortOrder, &sh.Transparent, &sh.State, &sh.InviteUID)
	return sh, err
}

func (s *Store) getShare(collectionID, userID int64) (Share, error) {
	sh, err := scanShare(s.DB.QueryRow(`SELECT `+shareCols+` FROM collection_shares WHERE collection_id = ? AND user_id = ?`, collectionID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return sh, ErrNotFound
	}
	return sh, err
}

// StaleShares lists the shares that have been in one of the states for more
// than days (counted from the invitation, or from the last answer or new
// invitation).
func (s *Store) StaleShares(days int, states ...string) ([]Share, error) {
	args := []any{days}
	for _, st := range states {
		args = append(args, st)
	}
	rows, err := s.DB.Query(`SELECT `+shareColsQ("sh")+` FROM collection_shares sh
		WHERE sh.state_changed_at < NOW() - INTERVAL ? DAY AND sh.state IN (?`+strings.Repeat(", ?", len(states)-1)+`) ORDER BY sh.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
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
		`SELECT `+shareColsQ("sh")+`,
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
		if err := rows.Scan(&x.ID, &x.CollectionID, &x.UserID, &x.Kind, &x.Slug, &x.Access, &x.DisplayName, &x.Color, &x.SortOrder, &x.Transparent, &x.State, &x.InviteUID,
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
	c, err = scanMount(s.DB.QueryRow(mountSelect+` WHERE sh.state = 'accepted' AND sh.user_id = ? AND sh.kind = ? AND sh.slug = ?`, userID, kind, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListMounts returns the collections of a kind that are shared with the user.
func (s *Store) ListMounts(userID int64, kind string) ([]Collection, error) {
	rows, err := s.DB.Query(mountSelect+` WHERE sh.state = 'accepted' AND sh.user_id = ? AND sh.kind = ? ORDER BY sh.sort_order, sh.slug`, userID, kind)
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
	c, err := scanMount(s.DB.QueryRow(mountSelect+` WHERE sh.state = 'accepted' AND sh.user_id = ? AND sh.id = ?`, userID, shareID))
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

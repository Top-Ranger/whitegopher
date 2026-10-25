package store

import (
	"time"
)

// SignupInvite is a pending single-use registration link. Only the hash of its
// token is stored.
type SignupInvite struct {
	ID        int64
	CreatedBy int64
	// CreatorName is the creator's username (filled by ListSignupInvites).
	CreatorName string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// CreateSignupInvite stores an invite token (by hash) and drops expired invites.
func (s *Store) CreateSignupInvite(createdBy int64, tokenHash string, expires time.Time) error {
	now := time.Now().Unix()
	if _, err := s.DB.Exec(`DELETE FROM invites WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.DB.Exec(`INSERT INTO invites (token_hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, createdBy, now, expires.Unix())
	return err
}

// SignupInviteValid reports whether a live invite has this token hash.
func (s *Store) SignupInviteValid(tokenHash string) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT count(*) FROM invites WHERE token_hash = ? AND expires_at >= ?`, tokenHash, time.Now().Unix()).Scan(&n)
	return n > 0, err
}

// ListSignupInvites returns live invites, newest first: all of them, or only those
// created by userID when it is non-zero.
func (s *Store) ListSignupInvites(userID int64) ([]SignupInvite, error) {
	rows, err := s.DB.Query(`SELECT i.id, i.created_by, u.username, i.created_at, i.expires_at
		FROM invites i JOIN users u ON u.id = i.created_by
		WHERE i.expires_at >= ? AND (? = 0 OR i.created_by = ?) ORDER BY i.id DESC`, time.Now().Unix(), userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignupInvite
	for rows.Next() {
		var i SignupInvite
		var c, e int64
		if err := rows.Scan(&i.ID, &i.CreatedBy, &i.CreatorName, &c, &e); err != nil {
			return nil, err
		}
		i.CreatedAt, i.ExpiresAt = time.Unix(c, 0), time.Unix(e, 0)
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteSignupInvite revokes an invite. With a non-zero userID only that user's
// invite can be revoked.
func (s *Store) DeleteSignupInvite(id, userID int64) error {
	res, err := s.DB.Exec(`DELETE FROM invites WHERE id = ? AND (? = 0 OR created_by = ?)`, id, userID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RegisterWithSignupInvite creates u and uses up the invite in one transaction, so
// a link cannot create two accounts and a failed registration (e.g. a taken
// username, ErrExists) leaves the invite intact. It returns ErrNotFound if the
// invite is gone or expired.
func (s *Store) RegisterWithSignupInvite(tokenHash string, u User) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM invites WHERE token_hash = ? AND expires_at >= ?`, tokenHash, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, ErrNotFound
	}
	res, err = tx.Exec(
		`INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin) VALUES (?, ?, ?, ?, ?, 0)`,
		u.Username, u.DisplayName, normEmail(u.Email), u.DigestHA1, u.PasswordHash)
	if isUnique(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

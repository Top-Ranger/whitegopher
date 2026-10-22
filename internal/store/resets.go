package store

import (
	"database/sql"
	"errors"
	"time"
)

// CreatePasswordReset stores a reset token (by hash) for the user, replacing
// any earlier one, and drops expired tokens.
func (s *Store) CreatePasswordReset(userID int64, tokenHash string, expires time.Time) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM password_resets WHERE user_id = ? OR expires_at < ?`, userID, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash, userID, expires.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// PasswordResetUser returns the user a live reset token belongs to.
func (s *Store) PasswordResetUser(tokenHash string) (User, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT user_id FROM password_resets WHERE token_hash = ? AND expires_at >= ?`, tokenHash, time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(id)
}

// ConsumePasswordReset deletes a token, returning false if it was already gone
// (so concurrent uses of one link cannot both succeed).
func (s *Store) ConsumePasswordReset(tokenHash string) (bool, error) {
	res, err := s.DB.Exec(`DELETE FROM password_resets WHERE token_hash = ? AND expires_at >= ?`, tokenHash, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

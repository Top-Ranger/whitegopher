package store

import (
	"database/sql"
	"errors"
	"time"
)

type Session struct {
	TokenHash string
	UserID    int64
	CSRF      string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(sess Session) error {
	// Opportunistically drop expired sessions.
	if _, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.DB.Exec(`INSERT INTO sessions (token_hash, user_id, csrf, expires_at) VALUES (?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, sess.CSRF, sess.ExpiresAt.Unix())
	return err
}

// SessionUser returns the live session and its user.
func (s *Store) SessionUser(tokenHash string) (Session, User, error) {
	var sess Session
	var exp int64
	err := s.DB.QueryRow(`SELECT token_hash, user_id, csrf, expires_at FROM sessions WHERE token_hash = ? AND expires_at >= ?`,
		tokenHash, time.Now().Unix()).Scan(&sess.TokenHash, &sess.UserID, &sess.CSRF, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, User{}, ErrNotFound
	}
	if err != nil {
		return sess, User{}, err
	}
	sess.ExpiresAt = time.Unix(exp, 0)
	u, err := s.UserByID(sess.UserID)
	return sess, u, err
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

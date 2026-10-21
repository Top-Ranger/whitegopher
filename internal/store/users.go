package store

import (
	"database/sql"
	"errors"
)

type User struct {
	ID           int64
	Username     string
	DisplayName  string
	Email        string
	DigestHA1    string
	PasswordHash string
	IsAdmin      bool
}

func (s *Store) CreateUser(u User) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin) VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.Email, u.DigestHA1, u.PasswordHash, u.IsAdmin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UserByName(name string) (User, error) {
	var u User
	err := s.DB.QueryRow(
		`SELECT id, username, display_name, email, digest_ha1, password_hash, is_admin FROM users WHERE username = ?`, name,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.DigestHA1, &u.PasswordHash, &u.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) SetPasswordHash(id int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

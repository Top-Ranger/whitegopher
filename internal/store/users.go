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

func (s *Store) UserByID(id int64) (User, error) {
	var u User
	err := s.DB.QueryRow(
		`SELECT id, username, display_name, email, digest_ha1, password_hash, is_admin FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.DigestHA1, &u.PasswordHash, &u.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query(
		`SELECT id, username, display_name, email, digest_ha1, password_hash, is_admin FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.DigestHA1, &u.PasswordHash, &u.IsAdmin); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CountUsers() (n int, err error) {
	err = s.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return
}

func (s *Store) CountAdmins() (n int, err error) {
	err = s.DB.QueryRow(`SELECT count(*) FROM users WHERE is_admin = 1`).Scan(&n)
	return
}

// CreateUserIfNone inserts u only when the users table is empty, so that
// first-run setup cannot be raced. It returns ErrExists otherwise.
func (s *Store) CreateUserIfNone(u User) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin)
		 SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.DisplayName, u.Email, u.DigestHA1, u.PasswordHash, u.IsAdmin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrExists
	}
	return res.LastInsertId()
}

// UpdateUser writes the profile fields of u.
func (s *Store) UpdateUser(u User) error {
	_, err := s.DB.Exec(`UPDATE users SET display_name = ?, email = ?, is_admin = ? WHERE id = ?`,
		u.DisplayName, u.Email, u.IsAdmin, u.ID)
	return err
}

// SetCredentials replaces both password representations and ends the
// user's sessions.
func (s *Store) SetCredentials(id int64, hash, ha1 string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ?, digest_ha1 = ? WHERE id = ?`, hash, ha1, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

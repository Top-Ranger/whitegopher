package store

import (
	"context"
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
	if s.d.isUnique(err) {
		return 0, ErrExists
	}
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
	var exec func(query string, args ...any) (sql.Result, error) = s.DB.Exec
	if s.d.name == DriverMySQL {
		// Concurrent INSERT..SELECT into an empty InnoDB table can fail with
		// auto-increment errors instead of losing cleanly; serialise with a
		// named lock held on one connection.
		ctx := context.Background()
		conn, err := s.DB.Conn(ctx)
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		var got sql.NullInt64
		if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('galdav_first_user', 10)`).Scan(&got); err != nil {
			return 0, err
		}
		if got.Int64 != 1 {
			return 0, errors.New("store: timed out waiting for first-user lock")
		}
		defer conn.ExecContext(ctx, `DO RELEASE_LOCK('galdav_first_user')`)
		exec = func(q string, args ...any) (sql.Result, error) { return conn.ExecContext(ctx, q, args...) }
	}
	res, err := exec(s.d.insertUserIfNone,
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

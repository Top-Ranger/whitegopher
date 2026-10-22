package store

import (
	"database/sql"
	"errors"
	"time"
)

// AppPassword is a per-device credential for DAV clients.
type AppPassword struct {
	ID         int64
	UserID     int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// CreateAppPassword stores the hash of a new app password.
func (s *Store) CreateAppPassword(userID int64, name, tokenHash string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO app_passwords (user_id, name, token_hash) VALUES (?, ?, ?)`, userID, name, tokenHash)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListAppPasswords(userID int64) ([]AppPassword, error) {
	rows, err := s.DB.Query(`SELECT id, user_id, name, UNIX_TIMESTAMP(created_at), UNIX_TIMESTAMP(last_used_at)
		FROM app_passwords WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppPassword
	for rows.Next() {
		var a AppPassword
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&a.ID, &a.UserID, &a.Name, &created, &used); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(created, 0)
		if used.Valid {
			t := time.Unix(used.Int64, 0)
			a.LastUsedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAppPassword revokes one of userID's app passwords.
func (s *Store) DeleteAppPassword(userID, id int64) error {
	res, err := s.DB.Exec(`DELETE FROM app_passwords WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseAppPassword reports whether tokenHash is one of userID's app passwords,
// and notes the use (at most once an hour per password).
func (s *Store) UseAppPassword(userID int64, tokenHash string) (bool, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM app_passwords WHERE user_id = ? AND token_hash = ?`, userID, tokenHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _ = s.DB.Exec(`UPDATE app_passwords SET last_used_at = NOW()
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at < NOW() - INTERVAL 1 HOUR)`, id)
	return true, nil
}

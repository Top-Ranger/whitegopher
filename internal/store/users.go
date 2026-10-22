package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type User struct {
	ID          int64
	Username    string
	DisplayName string
	// Email is stored lower-case; it is the user's calendar-user address.
	Email string
	// DigestHA1 is the md5 hash imported from Baikal. It only lets the user
	// sign in once; the first login replaces it with PasswordHash.
	DigestHA1    string
	PasswordHash string
	IsAdmin      bool
	// DefaultCalendar is the slug of the calendar receiving invitations;
	// empty means the first calendar.
	DefaultCalendar string
	// QuotaBytes caps the object data the user owns. nil means the server
	// default; 0 means unlimited.
	QuotaBytes *int64
	// HideFreeBusy withholds the user's availability from other users.
	HideFreeBusy bool
	// TOTPSecret is the shared secret of the user's authenticator app. It is
	// set while enrolling (TOTPEnabled false) and once two-factor is on.
	TOTPSecret   string
	TOTPEnabled  bool
	TOTPLastStep int64
}

const userCols = `id, username, display_name, email, digest_ha1, password_hash, is_admin, default_calendar, quota_bytes, hide_freebusy, totp_secret, totp_enabled, totp_last_step`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var quota sql.NullInt64
	err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.DigestHA1, &u.PasswordHash, &u.IsAdmin, &u.DefaultCalendar, &quota, &u.HideFreeBusy, &u.TOTPSecret, &u.TOTPEnabled, &u.TOTPLastStep)
	if quota.Valid {
		u.QuotaBytes = &quota.Int64
	}
	return u, err
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Store) CreateUser(u User) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin) VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, normEmail(u.Email), u.DigestHA1, u.PasswordHash, u.IsAdmin)
	if isUnique(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UserByName(name string) (User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// UserByEmail finds the user whose (lower-cased) email equals email.
func (s *Store) UserByEmail(email string) (User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ? ORDER BY id LIMIT 1`, normEmail(email)))
	if errors.Is(err, sql.ErrNoRows) || normEmail(email) == "" {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) SetPasswordHash(id int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash = ?, digest_ha1 = '' WHERE id = ?`, hash, id)
	return err
}

func (s *Store) UserByID(id int64) (User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
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
	res, err := conn.ExecContext(ctx,
		`INSERT INTO users (username, display_name, email, digest_ha1, password_hash, is_admin)
		 SELECT ?, ?, ?, ?, ?, ? FROM DUAL WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.DisplayName, normEmail(u.Email), u.DigestHA1, u.PasswordHash, u.IsAdmin)
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
	_, err := s.DB.Exec(`UPDATE users SET display_name = ?, email = ?, is_admin = ?, quota_bytes = ? WHERE id = ?`,
		u.DisplayName, normEmail(u.Email), u.IsAdmin, u.QuotaBytes, u.ID)
	return err
}

// SetCredentials replaces the password hash (dropping any imported HA1) and
// ends the user's sessions.
func (s *Store) SetCredentials(id int64, hash string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ?, digest_ha1 = '' WHERE id = ?`, hash, id); err != nil {
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

// SetDefaultCalendar records which calendar receives invitations.
func (s *Store) SetDefaultCalendar(userID int64, slug string) error {
	_, err := s.DB.Exec(`UPDATE users SET default_calendar = ? WHERE id = ?`, slug, userID)
	return err
}

// SetHideFreeBusy records whether the user's availability is hidden from others.
func (s *Store) SetHideFreeBusy(userID int64, hide bool) error {
	_, err := s.DB.Exec(`UPDATE users SET hide_freebusy = ? WHERE id = ?`, hide, userID)
	return err
}

// BeginTOTP stores a new, not yet confirmed secret.
func (s *Store) BeginTOTP(userID int64, secret string) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_secret = ?, totp_enabled = 0, totp_last_step = 0 WHERE id = ? AND totp_enabled = 0`, secret, userID)
	return err
}

// EnableTOTP turns two-factor on and stores the hashes of the recovery codes.
func (s *Store) EnableTOTP(userID int64, step int64, recoveryHashes []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_enabled = 1, totp_last_step = ? WHERE id = ? AND totp_secret <> ''`, step, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range recoveryHashes {
		if _, err := tx.Exec(`INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DisableTOTP removes the secret and recovery codes.
func (s *Store) DisableTOTP(userID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_step = 0 WHERE id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// AdvanceTOTPStep records that step was used, returning false if it (or a
// later one) already was, so a code cannot be replayed.
func (s *Store) AdvanceTOTPStep(userID, step int64) (bool, error) {
	res, err := s.DB.Exec(`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UseRecoveryCode consumes a recovery code, returning false if it is unknown.
func (s *Store) UseRecoveryCode(userID int64, hash string) (bool, error) {
	res, err := s.DB.Exec(`DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?`, userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) CountRecoveryCodes(userID int64) (n int, err error) {
	err = s.DB.QueryRow(`SELECT count(*) FROM recovery_codes WHERE user_id = ?`, userID).Scan(&n)
	return
}

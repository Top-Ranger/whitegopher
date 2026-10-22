package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
)

// PublicLink returns the token of the collection's public link, or ErrNotFound.
func (s *Store) PublicLink(collectionID int64) (string, error) {
	var tok string
	err := s.DB.QueryRow(`SELECT token FROM public_links WHERE collection_id = ?`, collectionID).Scan(&tok)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return tok, err
}

// NewPublicLink creates a public link for the collection, replacing any
// earlier one (so the old address stops working).
func (s *Store) NewPublicLink(collectionID int64) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	_, err := s.DB.Exec(`INSERT INTO public_links (collection_id, token) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE token = VALUES(token), created_at = CURRENT_TIMESTAMP`, collectionID, tok)
	return tok, err
}

// DeletePublicLink removes the collection's public link.
func (s *Store) DeletePublicLink(collectionID int64) error {
	_, err := s.DB.Exec(`DELETE FROM public_links WHERE collection_id = ?`, collectionID)
	return err
}

// CollectionByPublicToken finds the collection a public link points at.
func (s *Store) CollectionByPublicToken(token string) (Collection, error) {
	c, err := scanCollection(s.DB.QueryRow(`SELECT `+collCols+` FROM collections WHERE id = (SELECT collection_id FROM public_links WHERE token = ?)`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

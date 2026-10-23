// Package importer loads a White Gopher import tree (see docs/import-format.md)
// into a store. The format is source-neutral: converters for other servers
// write the tree, this package reads it.
package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bitflipp/whitegopher/internal/dav"
	"github.com/bitflipp/whitegopher/internal/index"
	"github.com/bitflipp/whitegopher/internal/store"
)

// Format and Version identify the manifest this package understands.
const (
	Format  = "whitegopher-import"
	Version = 1
)

// Result summarises an import run.
type Result struct {
	Users, Calendars, AddressBooks, Events, Cards int
	// Warnings lists objects imported without full indexing, and anything skipped.
	Warnings []string
}

func (r *Result) warnf(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

// Options controls an import.
type Options struct {
	// DryRun reads and validates everything but writes nothing.
	DryRun bool
}

type manifest struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	Generator string `json:"generator"`
}

type userFile struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	DigestHA1   string `json:"digest_ha1"`
}

type collectionFile struct {
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	Color       string            `json:"color"`
	SortOrder   int               `json:"sort_order"`
	Components  string            `json:"components"`
	Timezone    string            `json:"timezone"`
	Transparent bool              `json:"transparent"`
	ETags       map[string]string `json:"etags"`
}

// Import loads the tree rooted at dir into dst. It is idempotent: existing
// users and collections are reused and objects are overwritten by URI.
func Import(dir string, dst *store.Store, opt Options) (Result, error) {
	var res Result
	var m manifest
	if err := readJSON(filepath.Join(dir, "whitegopher-import.json"), &m); err != nil {
		return res, fmt.Errorf("manifest: %w", err)
	}
	if m.Format != Format {
		return res, fmt.Errorf("manifest: format %q, want %q", m.Format, Format)
	}
	if m.Version != Version {
		return res, fmt.Errorf("manifest: unsupported version %d (this build reads version %d)", m.Version, Version)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "users"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	im := &importer{dst: dst, opt: opt, res: &res}
	for _, e := range entries {
		if !e.IsDir() {
			res.warnf("users/%s: not a directory, ignored", e.Name())
			continue
		}
		if err := im.importUser(filepath.Join(dir, "users", e.Name()), e.Name()); err != nil {
			return res, err
		}
	}
	return res, nil
}

type importer struct {
	dst *store.Store
	opt Options
	res *Result
}

// readJSON decodes a file strictly: unknown fields are errors, so typos in a
// converter surface instead of silently dropping data.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// decodeName reverses the percent-encoding of directory and file names.
func decodeName(n string) (string, error) { return url.PathUnescape(n) }

func (im *importer) importUser(path, encName string) error {
	name, err := decodeName(encName)
	if err != nil || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\x00") {
		im.res.warnf("users/%s: invalid user name, skipped", encName)
		return nil
	}
	var uf userFile
	if err := readJSON(filepath.Join(path, "user.json"), &uf); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("user %q: %w", name, err)
	}
	owner := int64(-1)
	if im.opt.DryRun {
		im.res.Users++
	} else {
		u, err := im.dst.UserByName(name)
		switch {
		case err == nil:
			owner = u.ID
		case errors.Is(err, store.ErrNotFound):
			owner, err = im.dst.CreateUser(store.User{Username: name, DisplayName: uf.DisplayName, Email: uf.Email, DigestHA1: uf.DigestHA1})
			if err != nil {
				return fmt.Errorf("user %q: %w", name, err)
			}
			im.res.Users++
		default:
			return err
		}
	}
	for _, k := range []struct{ dir, kind string }{{"calendars", store.KindCalendar}, {"addressbooks", store.KindAddressBook}} {
		colls, err := os.ReadDir(filepath.Join(path, k.dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, c := range colls {
			if !c.IsDir() {
				continue
			}
			if err := im.importCollection(filepath.Join(path, k.dir, c.Name()), name, owner, k.kind, c.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (im *importer) importCollection(path, user string, owner int64, kind, encSlug string) error {
	slug, err := decodeName(encSlug)
	if err != nil || !dav.ValidSlug(slug) {
		im.res.warnf("%s: invalid or reserved collection name %q, skipped", user, encSlug)
		return nil
	}
	label := user + "/" + slug
	var cf collectionFile
	if err := readJSON(filepath.Join(path, "collection.json"), &cf); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", label, err)
	}
	comps := cf.Components
	if kind == store.KindCalendar && comps == "" {
		comps = "VEVENT,VTODO"
	}
	if kind == store.KindAddressBook {
		comps, cf.Timezone, cf.Transparent = "", "", false
	}
	id := int64(-1)
	if !im.opt.DryRun {
		existing, err := im.dst.GetCollection(owner, kind, slug)
		switch {
		case err == nil:
			id = existing.ID
		case errors.Is(err, store.ErrNotFound):
			id, err = im.dst.CreateCollection(store.Collection{
				OwnerID: owner, Kind: kind, Slug: slug, DisplayName: cf.DisplayName, Description: cf.Description,
				Color: cf.Color, SortOrder: cf.SortOrder, Components: comps, Timezone: cf.Timezone, Transparent: cf.Transparent,
			})
			if err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		default:
			return err
		}
	}
	if kind == store.KindCalendar {
		im.res.Calendars++
	} else {
		im.res.AddressBooks++
	}

	files, err := os.ReadDir(filepath.Join(path, "objects"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, f := range files {
		uri, err := decodeName(f.Name())
		if f.IsDir() || err != nil || uri == "" || len(uri) > 255 || uri == "." || uri == ".." || strings.ContainsAny(uri, "/\x00") {
			im.res.warnf("%s/objects/%s: invalid object name, skipped", label, f.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, "objects", f.Name()))
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			im.res.warnf("%s/%s: not valid UTF-8, skipped", label, uri)
			continue
		}
		var p index.Parsed
		var bad string
		if kind == store.KindCalendar {
			p, bad = index.Calendar(data, strings.Split(comps, ","))
			if bad != "" {
				p = index.Parsed{UID: uri, Component: "VEVENT"}
			}
			im.res.Events++
		} else {
			p, bad = index.Card(data, uri)
			if bad != "" {
				p = index.Parsed{UID: uri, Component: "VCARD"}
			}
			im.res.Cards++
		}
		if bad != "" {
			im.res.warnf("%s/%s: failed validation (%s); imported without index data", label, uri, bad)
		}
		if im.opt.DryRun {
			continue
		}
		etag := strings.Trim(cf.ETags[uri], `"`)
		if etag == "" {
			etag = index.ETag(data)
		}
		if _, err := im.dst.PutObject(store.Object{
			CollectionID: id, URI: uri, UID: p.UID, ETag: etag, Data: data,
			Component: p.Component, FirstOcc: p.First, LastOcc: p.Last,
		}, store.PutCond{AllowDupUID: true}); err != nil {
			return fmt.Errorf("%s/%s: %w", label, uri, err)
		}
	}
	return nil
}

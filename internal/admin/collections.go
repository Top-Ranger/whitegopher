package admin

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Top-Ranger/whitegopher/internal/dav"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)

type collView struct {
	store.Collection
	Count int
	IsCal bool
}

type dashboardData struct {
	Calendars, AddressBooks []collView
	// Shared collections other users have shared with this user.
	SharedCalendars, SharedBooks []collView
	// Invites are shares waiting for this user to accept.
	Invites []store.Invite
}

func (s *Server) views(userID int64, kind string) ([]collView, error) {
	cs, err := s.Store.ListCollections(userID, kind)
	if err != nil {
		return nil, err
	}
	return s.withCounts(cs, kind)
}

func (s *Server) mountViews(userID int64, kind string) ([]collView, error) {
	cs, err := s.Store.ListMounts(userID, kind)
	if err != nil {
		return nil, err
	}
	return s.withCounts(cs, kind)
}

func (s *Server) withCounts(cs []store.Collection, kind string) ([]collView, error) {
	out := make([]collView, 0, len(cs))
	for _, c := range cs {
		n, err := s.Store.ObjectCount(c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, collView{Collection: c, Count: n, IsCal: kind == store.KindCalendar})
	}
	return out, nil
}

func (s *Server) dashboardData(userID int64) (d dashboardData, err error) {
	if d.Calendars, err = s.views(userID, store.KindCalendar); err != nil {
		return
	}
	if d.AddressBooks, err = s.views(userID, store.KindAddressBook); err != nil {
		return
	}
	if d.SharedCalendars, err = s.mountViews(userID, store.KindCalendar); err != nil {
		return
	}
	if d.SharedBooks, err = s.mountViews(userID, store.KindAddressBook); err != nil {
		return
	}
	d.Invites, err = s.Store.PendingInvites(userID)
	return
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, sess *session) {
	d, err := s.dashboardData(sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "dashboard", http.StatusOK, page{Title: "Collections", Nav: "dashboard", Data: d})
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// slugFor derives a collection URL name from the requested slug or the
// display name, making it unique among the user's collections of that kind.
func (s *Server) slugFor(ownerID int64, kind, wanted, name string) (string, bool) {
	slug := strings.TrimSpace(wanted)
	explicit := slug != ""
	if !explicit {
		slug = strings.Trim(slugStrip.ReplaceAllString(strings.ToLower(name), "-"), "-")
		if len(slug) > 64 {
			slug = slug[:64]
		}
		if slug == "" {
			slug = randomHex(4)
		}
	}
	if !explicit && !dav.ValidSlug(slug) && dav.ValidSlug(slug+"-1") {
		slug += "-1" // e.g. a name that collides with a reserved one like "inbox"
	}
	if !dav.ValidSlug(slug) {
		return "", false
	}
	base := slug
	for i := 2; ; i++ {
		if taken, err := s.Store.SlugTaken(ownerID, kind, slug); err == nil && !taken {
			return slug, true
		}
		if explicit || i > 50 {
			return "", false
		}
		slug = base + "-" + strconv.Itoa(i)
	}
}

func (s *Server) dashboardError(w http.ResponseWriter, r *http.Request, sess *session, msg string) {
	d, _ := s.dashboardData(sess.User.ID)
	s.render(w, r, "dashboard", http.StatusUnprocessableEntity, page{Title: "Collections", Nav: "dashboard", Data: d, Error: msg})
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request, sess *session) {
	kind := r.PostFormValue("kind")
	if kind != store.KindCalendar && kind != store.KindAddressBook {
		http.Error(w, "bad kind", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		s.dashboardError(w, r, sess, "A name is required.")
		return
	}
	slug, ok := s.slugFor(sess.User.ID, kind, r.PostFormValue("slug"), name)
	if !ok {
		s.dashboardError(w, r, sess, "That URL name is invalid or already in use.")
		return
	}
	c := store.Collection{
		OwnerID: sess.User.ID, Kind: kind, Slug: slug, DisplayName: name,
		Description: strings.TrimSpace(r.PostFormValue("description")),
	}
	if kind == store.KindCalendar {
		color := strings.TrimSpace(r.PostFormValue("color"))
		if color != "" && !colorRE.MatchString(color) {
			s.dashboardError(w, r, sess, "Colour must look like #RRGGBB.")
			return
		}
		c.Color = color
		var comps []string
		for _, k := range []string{"VEVENT", "VTODO", "VJOURNAL"} {
			if r.PostFormValue("comp_"+k) != "" {
				comps = append(comps, k)
			}
		}
		if len(comps) == 0 {
			s.dashboardError(w, r, sess, "Select at least one component type.")
			return
		}
		c.Components = strings.Join(comps, ",")
	}
	if _, err := s.Store.CreateCollection(c); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/?ok=created", http.StatusSeeOther)
}

// ownedCollection loads the collection named by {id} if it belongs to the
// session user; otherwise it answers 404 (not revealing other users' ids).
func (s *Server) ownedCollection(w http.ResponseWriter, r *http.Request, sess *session) (store.Collection, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return store.Collection{}, false
	}
	c, err := s.Store.GetCollectionByID(id)
	if err != nil || c.OwnerID != sess.User.ID || c.Special != "" {
		if err != nil && !isNotFound(err) {
			s.fail(w, err)
		} else {
			notFound(w)
		}
		return store.Collection{}, false
	}
	return c, true
}

type collectionData struct {
	collView
	URL     string
	Sharees []store.Sharee
	// PublicURL is the collection's public link, if it has one.
	PublicURL string
}

func (s *Server) collectionPage(w http.ResponseWriter, r *http.Request, c store.Collection, status int, errMsg string) {
	n, _ := s.Store.ObjectCount(c.ID)
	area := "calendars"
	if c.Kind == store.KindAddressBook {
		area = "addressbooks"
	}
	sess, _ := s.currentSession(r)
	sharees, _ := s.Store.Sharees(c.ID)
	publicURL := ""
	if tok, err := s.Store.PublicLink(c.ID); err == nil {
		ext := ".ics"
		if c.Kind == store.KindAddressBook {
			ext = ".vcf"
		}
		publicURL = s.origin(r) + "/public/" + tok + ext
	}
	data := collectionData{
		collView:  collView{Collection: c, Count: n, IsCal: c.Kind == store.KindCalendar},
		URL:       s.davURL(r) + area + "/" + sess.User.Username + "/" + c.Slug + "/",
		Sharees:   sharees,
		PublicURL: publicURL,
	}
	s.render(w, r, "collection", status, page{Title: displayName(c), Nav: "dashboard", Data: data, Error: errMsg})
}

func displayName(c store.Collection) string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.Slug
}

func (s *Server) collectionForm(w http.ResponseWriter, r *http.Request, sess *session) {
	if c, ok := s.ownedCollection(w, r, sess); ok {
		s.collectionPage(w, r, c, http.StatusOK, "")
	}
}

func (s *Server) updateCollection(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedCollection(w, r, sess)
	if !ok {
		return
	}
	c.DisplayName = strings.TrimSpace(r.PostFormValue("name"))
	c.Description = strings.TrimSpace(r.PostFormValue("description"))
	if c.Kind == store.KindCalendar {
		color := strings.TrimSpace(r.PostFormValue("color"))
		if color != "" && !colorRE.MatchString(color) {
			s.collectionPage(w, r, c, http.StatusUnprocessableEntity, "Colour must look like #RRGGBB.")
			return
		}
		c.Color = color
		order, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("order")))
		if r.PostFormValue("order") != "" && err != nil {
			s.collectionPage(w, r, c, http.StatusUnprocessableEntity, "Order must be a number.")
			return
		}
		c.SortOrder = order
	}
	if err := s.Store.UpdateCollection(c); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/collections/"+strconv.FormatInt(c.ID, 10)+"?ok=saved", http.StatusSeeOther)
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedCollection(w, r, sess)
	if !ok {
		return
	}
	if r.PostFormValue("confirm") != c.Slug {
		s.collectionPage(w, r, c, http.StatusUnprocessableEntity, "Type the URL name to confirm deletion.")
		return
	}
	if err := s.Store.DeleteCollection(c.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/?ok=deleted", http.StatusSeeOther)
}

// --- sharing ----------------------------------------------------------------

func (s *Server) addShare(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedCollection(w, r, sess)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("username"))
	access := r.PostFormValue("access")
	if access != store.AccessRead && access != store.AccessReadWrite {
		http.Error(w, "bad access level", http.StatusBadRequest)
		return
	}
	target, err := s.Store.UserByName(name)
	switch {
	case isNotFound(err):
		s.collectionPage(w, r, c, http.StatusUnprocessableEntity, "No such user.")
		return
	case err != nil:
		s.fail(w, err)
		return
	case target.ID == sess.User.ID:
		s.collectionPage(w, r, c, http.StatusUnprocessableEntity, "You already own this collection.")
		return
	}
	if _, err := s.Sharing.Share(sess.User, c, target, access); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/collections/"+strconv.FormatInt(c.ID, 10)+"?ok=saved", http.StatusSeeOther)
}

func (s *Server) removeShare(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedCollection(w, r, sess)
	if !ok {
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	target, err := s.Store.UserByID(uid)
	if err == nil {
		err = s.Sharing.Unshare(sess.User, c, target)
	}
	if err != nil && !isNotFound(err) {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/collections/"+strconv.FormatInt(c.ID, 10)+"?ok=saved", http.StatusSeeOther)
}

// setPublicLink creates (or replaces) the read-only public link.
func (s *Server) setPublicLink(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedCollection(w, r, sess)
	if !ok {
		return
	}
	if _, err := s.Store.NewPublicLink(c.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/collections/"+strconv.FormatInt(c.ID, 10)+"?ok=saved#public", http.StatusSeeOther)
}

func (s *Server) removePublicLink(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedCollection(w, r, sess)
	if !ok {
		return
	}
	if err := s.Store.DeletePublicLink(c.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/collections/"+strconv.FormatInt(c.ID, 10)+"?ok=saved", http.StatusSeeOther)
}

// answerInvite accepts or declines a share offered to the session user.
func (s *Server) answerInvite(accept bool) handler {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			notFound(w)
			return
		}
		sh, err := s.Store.ShareByID(sess.User.ID, id)
		if err == nil {
			err = s.Sharing.Answer(sess.User, sh, accept)
		}
		if err != nil {
			if isNotFound(err) {
				notFound(w)
			} else {
				s.fail(w, err)
			}
			return
		}
		http.Redirect(w, r, "/admin/?ok=saved", http.StatusSeeOther)
	}
}

// ownedMount loads the share mount {id} if it belongs to the session user.
func (s *Server) ownedMount(w http.ResponseWriter, r *http.Request, sess *session) (store.Collection, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return store.Collection{}, false
	}
	c, err := s.Store.MountByID(sess.User.ID, id)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
		} else {
			s.fail(w, err)
		}
		return c, false
	}
	return c, true
}

func (s *Server) mountPage(w http.ResponseWriter, r *http.Request, c store.Collection, status int, errMsg string) {
	n, _ := s.Store.ObjectCount(c.ID)
	s.render(w, r, "shared", status, page{Title: displayName(c), Nav: "dashboard", Error: errMsg,
		Data: collView{Collection: c, Count: n, IsCal: c.Kind == store.KindCalendar}})
}

func (s *Server) mountForm(w http.ResponseWriter, r *http.Request, sess *session) {
	if c, ok := s.ownedMount(w, r, sess); ok {
		s.mountPage(w, r, c, http.StatusOK, "")
	}
}

func (s *Server) updateMount(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedMount(w, r, sess)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	color := strings.TrimSpace(r.PostFormValue("color"))
	order := 0
	if c.Kind == store.KindCalendar {
		if color != "" && !colorRE.MatchString(color) {
			s.mountPage(w, r, c, http.StatusUnprocessableEntity, "Colour must look like #RRGGBB.")
			return
		}
		if v := strings.TrimSpace(r.PostFormValue("order")); v != "" {
			var err error
			if order, err = strconv.Atoi(v); err != nil {
				s.mountPage(w, r, c, http.StatusUnprocessableEntity, "Order must be a number.")
				return
			}
		}
	} else {
		color = ""
	}
	// Clearing the name falls back to the owner's.
	if err := s.Store.UpdateShareProps(c.ShareID, name, color, order, false); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/shared/"+strconv.FormatInt(c.ShareID, 10)+"?ok=saved", http.StatusSeeOther)
}

func (s *Server) leaveMount(w http.ResponseWriter, r *http.Request, sess *session) {
	c, ok := s.ownedMount(w, r, sess)
	if !ok {
		return
	}
	if err := s.Store.DeleteShare(c.ID, sess.User.ID); err != nil && !isNotFound(err) {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/?ok=deleted", http.StatusSeeOther)
}

package dav

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	nsDAV  = "DAV:"
	nsCal  = "urn:ietf:params:xml:ns:caldav"
	nsCard = "urn:ietf:params:xml:ns:carddav"
	nsCS   = "http://calendarserver.org/ns/"
	nsApl  = "http://apple.com/ns/ical/"

	nsDecl = ` xmlns:D="DAV:" xmlns:C="` + nsCal + `" xmlns:A="` + nsCard + `" xmlns:S="` + nsCS + `" xmlns:I="` + nsApl + `"`
)

// propfind is a parsed PROPFIND request body.
type propfind struct {
	allProp  bool
	propName bool
	props    []xml.Name
	// calData narrows or expands calendar-data in REPORT responses.
	calData *calDataSpec
}

func parsePropfind(r io.Reader) (propfind, error) {
	var pf propfind
	body, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return pf, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		pf.allProp = true // RFC 4918: empty body means allprop
		return pf, nil
	}
	var doc struct {
		XMLName  xml.Name
		AllProp  *struct{} `xml:"DAV: allprop"`
		PropName *struct{} `xml:"DAV: propname"`
		Prop     *struct {
			Items []struct {
				XMLName xml.Name
			} `xml:",any"`
		} `xml:"DAV: prop"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return pf, err
	}
	pf.allProp = doc.AllProp != nil
	pf.propName = doc.PropName != nil
	if doc.Prop != nil {
		for _, it := range doc.Prop.Items {
			pf.props = append(pf.props, it.XMLName)
		}
	}
	return pf, nil
}

// resource is a DAV resource with its known properties; values are raw
// XML (already escaped).
type resource struct {
	href  string
	props map[xml.Name]string
	// status, when non-zero, makes this a status-only response (e.g. 404).
	status int
}

func esc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// hrefXMLRaw renders an already-formed address (e.g. mailto:) as an href.
func hrefXMLRaw(h string) string { return "<D:href>" + esc(h) + "</D:href>" }

// hrefXML renders a raw (unescaped) path as a DAV:href element.
func hrefXML(h string) string {
	return "<D:href>" + esc((&url.URL{Path: h}).EscapedPath()) + "</D:href>"
}

func qname(n xml.Name) string {
	switch n.Space {
	case nsDAV:
		return "D:" + n.Local
	case nsCal:
		return "C:" + n.Local
	case nsCard:
		return "A:" + n.Local
	case nsCS:
		return "S:" + n.Local
	case nsApl:
		return "I:" + n.Local
	}
	return n.Local + ` xmlns="` + esc(n.Space) + `"`
}

// writeMultistatus renders rs; trailer is raw XML appended inside the
// multistatus element (e.g. a sync-token).
func writeMultistatus(w http.ResponseWriter, pf propfind, rs []resource, trailer string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<D:multistatus` + nsDecl + `>`)
	for _, r := range rs {
		b.WriteString("<D:response>" + hrefXML(r.href))
		if r.status != 0 {
			b.WriteString("<D:status>HTTP/1.1 " + strconv.Itoa(r.status) + " " + http.StatusText(r.status) + "</D:status></D:response>")
			continue
		}
		var found, missing []xml.Name
		switch {
		case pf.propName || pf.allProp:
			for n := range r.props {
				found = append(found, n)
			}
			sort.Slice(found, func(i, j int) bool { return found[i].Space+found[i].Local < found[j].Space+found[j].Local })
		default:
			for _, n := range pf.props {
				if _, ok := r.props[n]; ok {
					found = append(found, n)
				} else {
					missing = append(missing, n)
				}
			}
		}
		if len(found) > 0 {
			b.WriteString("<D:propstat><D:prop>")
			for _, n := range found {
				if pf.propName {
					b.WriteString("<" + qname(n) + "/>")
				} else {
					b.WriteString("<" + qname(n) + ">" + r.props[n] + "</" + strings.SplitN(qname(n), " ", 2)[0] + ">")
				}
			}
			b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>")
		}
		if len(missing) > 0 {
			b.WriteString("<D:propstat><D:prop>")
			for _, n := range missing {
				b.WriteString("<" + qname(n) + "/>")
			}
			b.WriteString("</D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>")
		}
		b.WriteString("</D:response>")
	}
	b.WriteString(trailer + "</D:multistatus>")
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, b.String())
}

// propItem is a generic property element as found in PROPPATCH, MKCOL and
// MKCALENDAR bodies.
type propItem struct {
	XMLName  xml.Name
	Text     string `xml:",chardata"`
	Children []struct {
		XMLName xml.Name
		Name    string `xml:"name,attr"`
		Text    string `xml:",chardata"`
	} `xml:",any"`
}

// propUpdate is the shared shape of propertyupdate, mkcol and mkcalendar
// bodies; the root element name is ignored.
type propUpdate struct {
	Set []struct {
		Prop struct {
			Items []propItem `xml:",any"`
		} `xml:"DAV: prop"`
	} `xml:"DAV: set"`
	Remove []struct {
		Prop struct {
			Items []propItem `xml:",any"`
		} `xml:"DAV: prop"`
	} `xml:"DAV: remove"`
}

func parsePropUpdate(r io.Reader) (pu propUpdate, err error) {
	body, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return pu, err
	}
	return pu, xml.Unmarshal(body, &pu)
}

// writeError sends a DAV:error body carrying the given precondition
// element(s), e.g. "<C:valid-calendar-data/>".
func writeError(w http.ResponseWriter, status int, inner string) {
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(status)
	io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n<D:error"+nsDecl+">"+inner+"</D:error>")
}

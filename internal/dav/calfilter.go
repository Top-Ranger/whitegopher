package dav

import (
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/whitegopher/internal/index"
)

const icalUTC = "20060102T150405Z"

type timeRange struct {
	Start string `xml:"start,attr"`
	End   string `xml:"end,attr"`
}

// bounds returns the range; a zero time means open-ended.
func (tr *timeRange) bounds() (start, end time.Time, err error) {
	if tr.Start != "" {
		if start, err = time.Parse(icalUTC, tr.Start); err != nil {
			return
		}
	}
	if tr.End != "" {
		end, err = time.Parse(icalUTC, tr.End)
	}
	return
}

type textMatch struct {
	Text      string `xml:",chardata"`
	Collation string `xml:"collation,attr"`
	Negate    string `xml:"negate-condition,attr"`
}

type paramFilter struct {
	Name         string     `xml:"name,attr"`
	IsNotDefined *struct{}  `xml:"urn:ietf:params:xml:ns:caldav is-not-defined"`
	TextMatch    *textMatch `xml:"urn:ietf:params:xml:ns:caldav text-match"`
}

type propFilter struct {
	Name         string        `xml:"name,attr"`
	IsNotDefined *struct{}     `xml:"urn:ietf:params:xml:ns:caldav is-not-defined"`
	TimeRange    *timeRange    `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	TextMatch    *textMatch    `xml:"urn:ietf:params:xml:ns:caldav text-match"`
	ParamFilters []paramFilter `xml:"urn:ietf:params:xml:ns:caldav param-filter"`
}

type compFilter struct {
	Name         string       `xml:"name,attr"`
	IsNotDefined *struct{}    `xml:"urn:ietf:params:xml:ns:caldav is-not-defined"`
	TimeRange    *timeRange   `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	PropFilters  []propFilter `xml:"urn:ietf:params:xml:ns:caldav prop-filter"`
	CompFilters  []compFilter `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
}

func (tm *textMatch) matches(value string) bool {
	a, b := value, tm.Text
	if tm.Collation != "i;octet" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return strings.Contains(a, b) != (tm.Negate == "yes")
}

// matchComp reports whether component c satisfies filter f (whose name is
// assumed to have been matched against c by the caller).
func matchComp(c *ical.Component, f compFilter) bool {
	for _, pf := range f.PropFilters {
		if !matchProp(c, pf) {
			return false
		}
	}
	for _, cf := range f.CompFilters {
		var same []*ical.Component
		for _, ch := range c.Children {
			if strings.EqualFold(ch.Name, cf.Name) {
				same = append(same, ch)
			}
		}
		if cf.IsNotDefined != nil {
			if len(same) > 0 {
				return false
			}
			continue
		}
		var rs, re time.Time
		if cf.TimeRange != nil {
			var err error
			if rs, re, err = cf.TimeRange.bounds(); err != nil {
				return false
			}
		}
		found := false
		for _, ch := range same {
			if !matchComp(ch, compFilter{PropFilters: cf.PropFilters, CompFilters: cf.CompFilters}) {
				continue
			}
			if cf.TimeRange == nil || overlaps(ch, rs, re) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchProp(c *ical.Component, pf propFilter) bool {
	props := c.Props.Values(strings.ToUpper(pf.Name))
	if pf.IsNotDefined != nil {
		return len(props) == 0
	}
	for i := range props {
		p := &props[i]
		if pf.TextMatch != nil && !pf.TextMatch.matches(p.Value) {
			continue
		}
		if pf.TimeRange != nil {
			rs, re, err := pf.TimeRange.bounds()
			t, err2 := p.DateTime(time.UTC)
			if err != nil || err2 != nil || (!rs.IsZero() && t.Before(rs)) || (!re.IsZero() && !t.Before(re)) {
				continue
			}
		}
		okParams := true
		for _, pr := range pf.ParamFilters {
			vals := p.Params.Values(pr.Name)
			switch {
			case pr.IsNotDefined != nil:
				okParams = len(vals) == 0
			case len(vals) == 0:
				okParams = false
			case pr.TextMatch != nil:
				any := false
				for _, v := range vals {
					any = any || pr.TextMatch.matches(v)
				}
				okParams = any
			}
			if !okParams {
				break
			}
		}
		if okParams {
			return true
		}
	}
	return false
}

// overlapsSpan implements the RFC 4791 9.9 overlap test for one instance.
func overlapsSpan(s, e, rs, re time.Time) bool {
	if s.Equal(e) { // zero-length: start must lie in [rs, re)
		return (rs.IsZero() || !s.Before(rs)) && (re.IsZero() || s.Before(re))
	}
	return (re.IsZero() || s.Before(re)) && (rs.IsZero() || e.After(rs))
}

// overlaps reports whether any instance of c overlaps [rs, re). Recurrence
// overrides are evaluated as separate components, so an instance moved by an
// override can still match at its original time (a rare false positive).
func overlaps(c *ical.Component, rs, re time.Time) bool {
	s, e, ok := index.Span(c)
	if !ok {
		return true // can't tell; err on the side of inclusion
	}
	if overlapsSpan(s, e, rs, re) {
		return true
	}
	d := e.Sub(s)
	for _, p := range c.Props.Values(ical.PropRecurrenceDates) {
		if t, err := p.DateTime(time.UTC); err != nil || overlapsSpan(t, t.Add(d), rs, re) {
			return true
		}
	}
	if c.Props.Get(ical.PropRecurrenceRule) == nil {
		return false
	}
	set, err := c.RecurrenceSet(time.UTC)
	if err != nil || set == nil {
		return true
	}
	lo := rs.Add(-d)
	switch {
	case !re.IsZero():
		if rs.IsZero() {
			lo = s.Add(-time.Second)
		}
		for _, t := range set.Between(lo, re, true) {
			if overlapsSpan(t, t.Add(d), rs, re) {
				return true
			}
		}
		return false
	case !rs.IsZero():
		return !set.After(lo, true).IsZero()
	}
	return true
}

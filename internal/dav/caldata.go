package dav

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/whitegopher/internal/index"
	"github.com/bitflipp/whitegopher/internal/sched"
)

// calDataSpec is the content of a <C:calendar-data> element in a REPORT's
// DAV:prop (RFC 4791 9.6): which components and properties to return, and
// whether to expand or limit recurrences.
type calDataSpec struct {
	comp   *compSpec
	expand *timeSpan
	limit  *timeSpan
}

type compSpec struct {
	name     string
	allProp  bool
	allComp  bool
	props    map[string]bool
	children []*compSpec
}

type timeSpan struct{ start, end time.Time }

type compXML struct {
	Name    string    `xml:"name,attr"`
	AllProp *struct{} `xml:"urn:ietf:params:xml:ns:caldav allprop"`
	AllComp *struct{} `xml:"urn:ietf:params:xml:ns:caldav allcomp"`
	Props   []struct {
		Name string `xml:"name,attr"`
	} `xml:"urn:ietf:params:xml:ns:caldav prop"`
	Comps []compXML `xml:"urn:ietf:params:xml:ns:caldav comp"`
}

type spanXML struct {
	Start string `xml:"start,attr"`
	End   string `xml:"end,attr"`
}

// newCalData builds the spec from the children of a calendar-data element.
// It returns nil when there is nothing to apply (the whole object is wanted).
func newCalData(comp *compXML, expand, limit *spanXML) *calDataSpec {
	spec := &calDataSpec{expand: expand.span(), limit: limit.span()}
	if comp != nil {
		spec.comp = comp.spec()
	}
	if spec.comp == nil && spec.expand == nil && spec.limit == nil {
		return nil
	}
	return spec
}

func (c compXML) spec() *compSpec {
	s := &compSpec{name: strings.ToUpper(c.Name), allProp: c.AllProp != nil, allComp: c.AllComp != nil, props: map[string]bool{}}
	for _, p := range c.Props {
		s.props[strings.ToUpper(p.Name)] = true
	}
	for _, ch := range c.Comps {
		s.children = append(s.children, ch.spec())
	}
	return s
}

func (x *spanXML) span() *timeSpan {
	if x == nil {
		return nil
	}
	start, err1 := time.Parse("20060102T150405Z", x.Start)
	end, err2 := time.Parse("20060102T150405Z", x.End)
	if err1 != nil || err2 != nil || !end.After(start) {
		return nil
	}
	return &timeSpan{start, end}
}

// render applies the spec to stored calendar data. Anything it cannot handle
// returns the data untouched, which is always a valid answer.
func (spec *calDataSpec) render(data []byte) []byte {
	if spec == nil {
		return data
	}
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return data
	}
	switch {
	case spec.expand != nil:
		if !expandCalendar(cal, *spec.expand) {
			return data
		}
	case spec.limit != nil:
		limitRecurrences(cal, *spec.limit)
	}
	if spec.comp != nil {
		if spec.comp.name != ical.CompCalendar {
			return data
		}
		cal = &ical.Calendar{Component: spec.comp.filter(cal.Component)}
	}
	return sched.Encode(cal)
}

// filter returns a copy of c restricted to what the spec asks for.
func (s *compSpec) filter(c *ical.Component) *ical.Component {
	out := ical.NewComponent(c.Name)
	for name, props := range c.Props {
		if s.allProp || s.props[strings.ToUpper(name)] {
			out.Props[name] = append([]ical.Prop(nil), props...)
		}
	}
	for _, ch := range c.Children {
		if s.allComp {
			out.Children = append(out.Children, sched.CloneComponent(ch))
			continue
		}
		for _, sub := range s.children {
			if sub.name == strings.ToUpper(ch.Name) {
				out.Children = append(out.Children, sub.filter(ch))
				break
			}
		}
	}
	return out
}

// limitRecurrences keeps the master of each series but only the overrides
// that overlap the span (limit-recurrence-set).
func limitRecurrences(cal *ical.Calendar, sp timeSpan) {
	kept := cal.Children[:0]
	for _, c := range cal.Children {
		if c.Props.Get(ical.PropRecurrenceID) != nil {
			if s, e, ok := index.Span(c); ok && !overlapsSpan(s, e, sp.start, sp.end) {
				continue
			}
		}
		kept = append(kept, c)
	}
	cal.Children = kept
}

// expandCalendar replaces each recurring series by its instances overlapping
// the span (RFC 4791 9.6.5): one component per instance with a RECURRENCE-ID,
// no recurrence properties and no VTIMEZONE. It reports false when the object
// cannot be expanded reliably (e.g. unknown time zones).
func expandCalendar(cal *ical.Calendar, sp timeSpan) bool {
	var out []*ical.Component
	var series []*ical.Component
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			continue
		}
		series = append(series, c)
	}
	var master *ical.Component
	var overrides []*ical.Component
	for _, c := range series {
		switch {
		case c.Props.Get(ical.PropRecurrenceID) != nil:
			overrides = append(overrides, c)
		case master == nil:
			master = c
		default:
			out = append(out, c) // a second, unrelated component: leave alone
		}
	}
	if master == nil || (master.Props.Get(ical.PropRecurrenceRule) == nil && master.Props.Get(ical.PropRecurrenceDates) == nil) {
		// Not recurring: pass through (still without VTIMEZONE if all-UTC).
		return true
	}
	if master.Name != ical.CompEvent && master.Name != ical.CompToDo {
		return true
	}
	start, end, ok := index.Span(master)
	if !ok || master.Props.Get(ical.PropDateTimeStart) == nil {
		return false
	}
	d := end.Sub(start)
	dtstart := master.Props.Get(ical.PropDateTimeStart)
	mode := timeMode(dtstart)

	var starts []time.Time
	if master.Props.Get(ical.PropRecurrenceRule) != nil {
		set, err := master.RecurrenceSet(time.UTC)
		if err != nil || set == nil {
			return false
		}
		starts = set.Between(sp.start.Add(-d), sp.end, true)
	} else {
		starts = append(starts, start)
		for _, p := range master.Props.Values(ical.PropRecurrenceDates) {
			t, err := p.DateTime(time.UTC)
			if err != nil {
				return false
			}
			starts = append(starts, t)
		}
		exdates := map[int64]bool{}
		for _, p := range master.Props.Values(ical.PropExceptionDates) {
			if t, err := p.DateTime(time.UTC); err == nil {
				exdates[t.Unix()] = true
			}
		}
		kept := starts[:0]
		for _, t := range starts {
			if !exdates[t.Unix()] {
				kept = append(kept, t)
			}
		}
		starts = kept
	}

	overridden := map[int64]bool{}
	for _, o := range overrides {
		rid, err := o.Props.Get(ical.PropRecurrenceID).DateTime(time.UTC)
		if err != nil {
			return false
		}
		overridden[rid.Unix()] = true
		if s, e, ok := index.Span(o); ok && overlapsSpan(s, e, sp.start, sp.end) {
			out = append(out, instanceOf(o, o, rid, s, e.Sub(s), mode))
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	seen := map[int64]bool{}
	for _, t := range starts {
		if seen[t.Unix()] || overridden[t.Unix()] || !overlapsSpan(t, t.Add(d), sp.start, sp.end) {
			continue
		}
		seen[t.Unix()] = true
		out = append(out, instanceOf(master, master, t, t, d, mode))
	}
	cal.Children = out
	return true
}

// timeMode says how an instance's times are written: "date" for all-day
// events, "utc" for anchored times, "floating" for floating times.
func timeMode(p *ical.Prop) string {
	switch {
	case p.ValueType() == ical.ValueDate:
		return "date"
	case p.Params.Get(ical.ParamTimezoneID) == "" && !strings.HasSuffix(p.Value, "Z"):
		return "floating"
	}
	return "utc"
}

// instanceOf builds the component for one instance of base starting at start,
// identified by rid. src supplies the properties.
func instanceOf(base, src *ical.Component, rid, start time.Time, d time.Duration, mode string) *ical.Component {
	c := sched.CloneComponent(src)
	for _, name := range []string{ical.PropRecurrenceRule, ical.PropRecurrenceDates, "EXRULE", ical.PropExceptionDates,
		ical.PropRecurrenceID, ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropDuration, ical.PropDue} {
		c.Props.Del(name)
	}
	put := func(name string, t time.Time) {
		p := ical.Prop{Name: name, Params: ical.Params{}}
		switch mode {
		case "date":
			p.Value = t.Format("20060102")
			p.Params.Set(ical.ParamValue, "DATE")
		case "floating":
			p.Value = t.Format("20060102T150405")
		default:
			p.Value = t.UTC().Format("20060102T150405Z")
		}
		c.Props.Set(&p)
	}
	put(ical.PropRecurrenceID, rid)
	put(ical.PropDateTimeStart, start)
	if base.Name == ical.CompToDo && src.Props.Get(ical.PropDue) != nil {
		put(ical.PropDue, start.Add(d))
	} else if d > 0 || src.Props.Get(ical.PropDateTimeEnd) != nil || src.Props.Get(ical.PropDuration) != nil {
		put(ical.PropDateTimeEnd, start.Add(d))
	}
	return c
}

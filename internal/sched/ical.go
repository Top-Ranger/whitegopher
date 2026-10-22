// Package sched implements free-busy computation and CalDAV scheduling
// (RFC 6638 / iTIP RFC 5546): delivery of invitations, replies and
// cancellations between local users, plus optional iMIP email for others.
package sched

import (
	"bytes"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-ical"
)

const prodID = "-//whitegopher//CalDAV//EN"

func decode(data []byte) (*ical.Calendar, error) {
	return ical.NewDecoder(bytes.NewReader(data)).Decode()
}

// normAddr canonicalises a calendar user address: mailto: addresses are
// case-insensitive.
func normAddr(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 7 && strings.EqualFold(v[:7], "mailto:") {
		return "mailto:" + strings.ToLower(v[7:])
	}
	return v
}

func cloneComp(c *ical.Component) *ical.Component {
	out := ical.NewComponent(c.Name)
	for name, props := range c.Props {
		for _, p := range props {
			np := ical.Prop{Name: p.Name, Value: p.Value, Params: ical.Params{}}
			for k, v := range p.Params {
				np.Params[k] = append([]string(nil), v...)
			}
			out.Props[name] = append(out.Props[name], np)
		}
	}
	for _, ch := range c.Children {
		out.Children = append(out.Children, cloneComp(ch))
	}
	return out
}

// schedulable returns the non-timezone components of cal.
func schedulable(cal *ical.Calendar) []*ical.Component {
	var out []*ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			out = append(out, c)
		}
	}
	return out
}

func newCalendar(method string) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, prodID)
	if method != "" {
		cal.Props.SetText(ical.PropMethod, method)
	}
	return cal
}

// newProp builds a property with raw value and optional key,value params.
func newProp(name, value string, params ...string) ical.Prop {
	p := ical.Prop{Name: name, Value: value, Params: ical.Params{}}
	for i := 0; i+1 < len(params); i += 2 {
		p.Params.Set(params[i], params[i+1])
	}
	return p
}

func setProp(c *ical.Component, p ical.Prop) { c.Props[p.Name] = []ical.Prop{p} }

func getValue(c *ical.Component, name string) string {
	if p := c.Props.Get(name); p != nil {
		return p.Value
	}
	return ""
}

func sequenceOf(c *ical.Component) int {
	if p := c.Props.Get(ical.PropSequence); p != nil {
		if n, err := p.Int(); err == nil {
			return n
		}
	}
	return 0
}

func maxSequence(cs []*ical.Component) int {
	m := 0
	for _, c := range cs {
		if s := sequenceOf(c); s > m {
			m = s
		}
	}
	return m
}

// scheduleAgentServer reports whether the server is responsible for
// delivery to the party named by p (SCHEDULE-AGENT absent or SERVER).
func scheduleAgentServer(p *ical.Prop) bool {
	a := p.Params.Get("SCHEDULE-AGENT")
	return a == "" || strings.EqualFold(a, "SERVER")
}

// --- encoding ---------------------------------------------------------------
//
// go-ical's encoder rejects some valid real-world objects, so scheduling
// writes its own small serialiser (RFC 5545 3.1 folding, parameter quoting).

func encode(cal *ical.Calendar) []byte {
	var b bytes.Buffer
	writeComp(&b, cal.Component)
	return b.Bytes()
}

var propOrder = map[string]int{"VERSION": 0, "PRODID": 1, "METHOD": 2, "UID": 3}

func writeComp(b *bytes.Buffer, c *ical.Component) {
	writeLine(b, "BEGIN", c.Name)
	names := make([]string, 0, len(c.Props))
	for n := range c.Props {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		oi, iok := propOrder[names[i]]
		oj, jok := propOrder[names[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		for _, p := range c.Props[n] {
			var params strings.Builder
			keys := make([]string, 0, len(p.Params))
			for k := range p.Params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				vals := make([]string, 0, len(p.Params[k]))
				for _, v := range p.Params[k] {
					v = strings.ReplaceAll(v, `"`, `'`)
					if strings.ContainsAny(v, ";:,") {
						v = `"` + v + `"`
					}
					vals = append(vals, v)
				}
				params.WriteString(";" + k + "=" + strings.Join(vals, ","))
			}
			writeLine(b, n+params.String(), p.Value)
		}
	}
	for _, ch := range c.Children {
		writeComp(b, ch)
	}
	writeLine(b, "END", c.Name)
}

// writeLine writes "name:value" folded at 75 octets without splitting runes.
func writeLine(b *bytes.Buffer, name, value string) {
	line := name + ":" + value
	first := true
	for len(line) > 0 {
		limit := 75
		if !first {
			limit = 74 // leading space counts
		}
		if len(line) <= limit {
			if !first {
				b.WriteByte(' ')
			}
			b.WriteString(line + "\r\n")
			return
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		if !first {
			b.WriteByte(' ')
		}
		b.WriteString(line[:cut] + "\r\n")
		line = line[cut:]
		first = false
	}
}

func utc(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// Encode serialises cal with whitegopher's own writer (see above).
func Encode(cal *ical.Calendar) []byte { return encode(cal) }

// CloneComponent returns a deep copy of c.
func CloneComponent(c *ical.Component) *ical.Component { return cloneComp(c) }

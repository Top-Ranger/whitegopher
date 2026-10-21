package dav

import (
	"strings"

	"github.com/emersion/go-vcard"
)

type cardTextMatch struct {
	Text      string `xml:",chardata"`
	Collation string `xml:"collation,attr"`
	MatchType string `xml:"match-type,attr"`
	Negate    string `xml:"negate-condition,attr"`
}

func (tm cardTextMatch) matches(value string) bool {
	a, b := value, tm.Text
	if tm.Collation != "i;octet" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	var ok bool
	switch tm.MatchType {
	case "equals":
		ok = a == b
	case "starts-with":
		ok = strings.HasPrefix(a, b)
	case "ends-with":
		ok = strings.HasSuffix(a, b)
	default:
		ok = strings.Contains(a, b)
	}
	return ok != (tm.Negate == "yes")
}

type cardParamFilter struct {
	Name         string         `xml:"name,attr"`
	IsNotDefined *struct{}      `xml:"urn:ietf:params:xml:ns:carddav is-not-defined"`
	TextMatch    *cardTextMatch `xml:"urn:ietf:params:xml:ns:carddav text-match"`
}

type cardPropFilter struct {
	Name         string            `xml:"name,attr"`
	Test         string            `xml:"test,attr"`
	IsNotDefined *struct{}         `xml:"urn:ietf:params:xml:ns:carddav is-not-defined"`
	TextMatches  []cardTextMatch   `xml:"urn:ietf:params:xml:ns:carddav text-match"`
	ParamFilters []cardParamFilter `xml:"urn:ietf:params:xml:ns:carddav param-filter"`
}

type cardFilterSpec struct {
	Test        string           `xml:"test,attr"`
	PropFilters []cardPropFilter `xml:"urn:ietf:params:xml:ns:carddav prop-filter"`
}

func (f cardFilterSpec) match(card vcard.Card) bool {
	if len(f.PropFilters) == 0 {
		return true
	}
	all := f.Test == "allof"
	for _, pf := range f.PropFilters {
		if pf.match(card) == !all {
			return !all
		}
	}
	return all
}

func (pf cardPropFilter) match(card vcard.Card) bool {
	var fields []*vcard.Field
	for k, v := range card {
		if strings.EqualFold(k, pf.Name) {
			fields = append(fields, v...)
		}
	}
	if pf.IsNotDefined != nil {
		return len(fields) == 0
	}
	if len(pf.TextMatches) == 0 && len(pf.ParamFilters) == 0 {
		return len(fields) > 0
	}
	all := pf.Test == "allof"
	for _, fld := range fields {
		conds := make([]bool, 0, len(pf.TextMatches)+len(pf.ParamFilters))
		for _, tm := range pf.TextMatches {
			conds = append(conds, tm.matches(fld.Value))
		}
		for _, pr := range pf.ParamFilters {
			vals := fld.Params[pr.Name]
			switch {
			case pr.IsNotDefined != nil:
				conds = append(conds, len(vals) == 0)
			case pr.TextMatch != nil:
				any := false
				for _, v := range vals {
					any = any || pr.TextMatch.matches(v)
				}
				conds = append(conds, any)
			default:
				conds = append(conds, len(vals) > 0)
			}
		}
		res := all
		for _, c := range conds {
			if all {
				res = res && c
			} else {
				res = res || c
			}
		}
		if res {
			return true
		}
	}
	return false
}

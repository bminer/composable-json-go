package composablejson

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// anchor returns the node named name in d, in two passes: first among the
// anchors written in d, then among those d imports from other documents.
func (c *call) anchor(d *document, name string, from *node) (value, error) {
	switch ns := d.written[name]; len(ns) {
	case 0:
	case 1:
		return c.position(ns[0])
	default:
		return nil, &Error{
			Kind:     ErrDuplicateAnchor,
			Location: from.loc(),
			Detail:   fmt.Sprintf("%q is declared more than once", name),
			Chain:    []Location{ns[0].loc(), ns[1].loc()},
		}
	}
	imports, err := c.imports(d)
	if err != nil {
		return nil, err
	}
	switch cs := imports[name]; len(cs) {
	case 0:
		return nil, errorf(ErrUnresolvable, from, "no anchor %q in %s", name, displayURI(d))
	case 1:
		return cs[0].v, nil
	default:
		return nil, &Error{
			Kind:     ErrDuplicateAnchor,
			Location: from.loc(),
			Detail:   fmt.Sprintf("%q is imported more than once", name),
			Chain:    []Location{cs[0].loc, cs[1].loc},
		}
	}
}

func displayURI(d *document) string {
	if d.key == "" {
		return "the document"
	}
	return d.key
}

// A candidate is a node that an import brings into a document with an
// anchor.
type candidate struct {
	v   value    // the node's value at its position in the document
	loc Location // the directive that imports it
	id  string   // distinguishes nodes; equal for one node imported twice
}

// imports resolves every reference in d to another document, deferring those
// within d, and returns the anchored nodes they bring in, by name.
func (c *call) imports(d *document) (map[string][]candidate, error) {
	st := c.state(d)
	if st.imports != nil {
		return st.imports, nil
	}
	g := guard{d: d, op: opImports}
	if err := c.enter(g); err != nil {
		return nil, err
	}
	defer c.leave(g)
	found := map[string][]candidate{}
	for _, dn := range d.directives {
		cl, err := dn.class()
		if err != nil {
			return nil, err
		}
		var refs []string
		switch cl {
		case cRef:
			refs = []string{dn.obj["$ref"].val.(string)}
		case cExtend:
			refs = dn.extendRefs()
		case cSplice:
			if dn.parent == nil || dn.parent.kind != arrayNode {
				continue
			}
			refs, _ = dn.refList("$splice")
		}
		for i, ref := range refs {
			t, err := c.target(dn, ref)
			if err != nil {
				return nil, err
			}
			if t.uri == nil {
				continue
			}
			if err := c.collectImports(found, dn, cl, ref, i); err != nil {
				return nil, err
			}
		}
	}
	st.imports = found
	return found, nil
}

// collectImports adds to found the anchored nodes that the i'th reference of
// directive dn brings in.
func (c *call) collectImports(found map[string][]candidate, dn *node, cl class, ref string, i int) error {
	t, err := c.reference(dn, ref)
	if err != nil {
		return err
	}
	v, err := t.full(c)
	if err != nil {
		return err
	}
	b := c.budget()
	collect := func(at value, v any, loc Location, id string) error {
		var err error
		scanAnchors(v, nil, b, func(name string, p Pointer) bool {
			n, ok, e := c.walk(at, p)
			if e == nil && ok {
				var got string
				got, e = anchorName(c, n)
				ok = got == name
			}
			if e != nil {
				err = e
				return false
			}
			if !ok {
				// The document's own keys replaced the imported node.
				return true
			}
			cd := candidate{v: n, loc: loc, id: id + p.String()}
			if cd.loc.Pointer == nil {
				cd.loc.Pointer = append(dn.pointer(), p...)
			}
			if !slices.ContainsFunc(found[name], func(x candidate) bool { return x.id == cd.id }) {
				found[name] = append(found[name], cd)
			}
			return true
		})
		if err == nil && b.exceeded() {
			err = b.err(dn)
		}
		return err
	}
	if cl == cSplice {
		// A spliced element merges with nothing, so it is found in the
		// referenced array itself, without working out where it lands.
		arr, _ := v.([]any)
		for j, item := range arr {
			id := fmt.Sprintf("%s\x00%d\x00%d", dn.pointer(), i, j)
			if err := collect(plain{item}, item, dn.loc(), id); err != nil {
				return err
			}
		}
		return nil
	}
	at, err := c.position(dn)
	if err != nil {
		return err
	}
	return collect(at, v, Location{URI: dn.doc.key}, dn.pointer().String()+"\x00")
}

// anchorName returns the $anchor of v, or "" if it has none.
func anchorName(c *call, v value) (string, error) {
	k, err := v.kind(c)
	if err != nil || k != vObject {
		return "", err
	}
	a, err := v.get(c, "$anchor")
	if err != nil {
		return "", err
	}
	if k, err := a.kind(c); err != nil || k != vScalar {
		return "", err
	}
	x, err := a.full(c)
	s, _ := x.(string)
	return s, err
}

// scanAnchors calls fn with the name and position of every object in v that
// carries an $anchor, in a fixed order, until fn returns false or b runs out.
// It reports whether the scan finished.
func scanAnchors(v any, p Pointer, b *budget, fn func(name string, p Pointer) bool) bool {
	if !b.spend() {
		return false
	}
	switch v := v.(type) {
	case map[string]any:
		if s, ok := v["$anchor"].(string); ok && !fn(s, slices.Clone(p)) {
			return false
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if !scanAnchors(v[k], append(p, k), b, fn) {
				return false
			}
		}
	case []any:
		for i, x := range v {
			if !scanAnchors(x, append(p, strconv.Itoa(i)), b, fn) {
				return false
			}
		}
	}
	return true
}

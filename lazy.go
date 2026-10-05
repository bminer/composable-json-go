package composablejson

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// A value is a resolved value computed only as far as it is needed. This is
// how the resolver follows the specification's rule that reaching a target
// resolves only the path to it: stepping into a value with get or index
// resolves nothing beside the step, and only full resolves everything.
//
// get and keys are meaningful only for vObject, length and index only for
// vArray. A missing member is the absent value, never nil.
type value interface {
	kind(c *call) (vkind, error)
	get(c *call, key string) (value, error)
	keys(c *call) ([]string, error)
	length(c *call) (int, error)
	index(c *call, i int) (value, error)
	full(c *call) (any, error)
}

type vkind uint8

const (
	vAbsent vkind = iota // no value: a missing key, or one a null deleted
	vDelete              // a null written in an $extend node, which deletes
	vScalar
	vObject
	vArray
)

// marker is the absent or deleting value.
type marker vkind

var (
	absent  value = marker(vAbsent)
	deleted value = marker(vDelete)
)

func (m marker) kind(*call) (vkind, error)      { return vkind(m), nil }
func (marker) get(*call, string) (value, error) { return absent, nil }
func (marker) keys(*call) ([]string, error)     { return nil, nil }
func (marker) length(*call) (int, error)        { return 0, nil }
func (marker) index(*call, int) (value, error)  { return absent, nil }
func (marker) full(*call) (any, error)          { return nil, nil }

// plain is a value that is already fully resolved.
type plain struct{ v any }

func plainKind(v any) vkind {
	switch v.(type) {
	case map[string]any:
		return vObject
	case []any:
		return vArray
	}
	return vScalar
}

func (p plain) kind(*call) (vkind, error) { return plainKind(p.v), nil }

func (p plain) get(_ *call, key string) (value, error) {
	if m, ok := p.v.(map[string]any); ok {
		if x, ok := m[key]; ok {
			return plain{x}, nil
		}
	}
	return absent, nil
}

func (p plain) keys(*call) ([]string, error) {
	m, _ := p.v.(map[string]any)
	return slices.Sorted(maps.Keys(m)), nil
}

func (p plain) length(*call) (int, error) {
	a, _ := p.v.([]any)
	return len(a), nil
}

func (p plain) index(_ *call, i int) (value, error) {
	if a, ok := p.v.([]any); ok && i >= 0 && i < len(a) {
		return plain{a[i]}, nil
	}
	return absent, nil
}

func (p plain) full(*call) (any, error) { return p.v, nil }

// nodeValue is the value of a node, on its own: what a reference to its
// position would select if no $extend above it merged anything in. A node
// under an $extend's own keys is merged by that $extend; see position.
type nodeValue struct {
	n *node

	target   value       // $ref: the referenced value
	merged   *mergeValue // $extend: references merged with own keys
	elems    []value     // array: elements after splicing
	expanded bool

	val  any
	done bool
}

func (v *nodeValue) kind(c *call) (vkind, error) {
	cl, err := v.n.class()
	if err != nil {
		return 0, err
	}
	switch cl {
	case cScalar:
		return vScalar, nil
	case cArray:
		return vArray, nil
	case cPlain, cExtend:
		return vObject, nil
	case cRef:
		t, err := v.ref(c)
		if err != nil {
			return 0, err
		}
		g := guard{n: v.n, op: opKind}
		if err := c.enter(g); err != nil {
			return 0, err
		}
		defer c.leave(g)
		return t.kind(c)
	}
	return 0, spliceOutside(v.n)
}

func (v *nodeValue) get(c *call, key string) (value, error) {
	cl, err := v.n.class()
	if err != nil {
		return nil, err
	}
	switch cl {
	case cPlain:
		if err := c.checkKey(v.n, key); err != nil {
			return nil, err
		}
		ch, ok := v.n.obj[key]
		if !ok {
			return absent, nil
		}
		return c.valueOf(ch), nil
	case cExtend:
		m, err := v.merge(c)
		if err != nil {
			return nil, err
		}
		return m.get(c, key)
	case cRef:
		t, err := v.ref(c)
		if err != nil {
			return nil, err
		}
		g := guard{n: v.n, op: opGet, arg: key}
		if err := c.enter(g); err != nil {
			return nil, err
		}
		defer c.leave(g)
		return t.get(c, key)
	}
	return absent, nil
}

func (v *nodeValue) keys(c *call) ([]string, error) {
	cl, err := v.n.class()
	if err != nil {
		return nil, err
	}
	switch cl {
	case cPlain:
		for _, k := range v.n.keys {
			if err := c.checkKey(v.n, k); err != nil {
				return nil, err
			}
		}
		return v.n.keys, nil
	case cExtend:
		m, err := v.merge(c)
		if err != nil {
			return nil, err
		}
		return m.keys(c)
	case cRef:
		t, err := v.ref(c)
		if err != nil {
			return nil, err
		}
		g := guard{n: v.n, op: opKeys}
		if err := c.enter(g); err != nil {
			return nil, err
		}
		defer c.leave(g)
		return t.keys(c)
	}
	return nil, nil
}

func (v *nodeValue) length(c *call) (int, error) {
	cl, err := v.n.class()
	if err != nil {
		return 0, err
	}
	switch cl {
	case cArray:
		if err := v.expand(c); err != nil {
			return 0, err
		}
		return len(v.elems), nil
	case cRef:
		t, err := v.ref(c)
		if err != nil {
			return 0, err
		}
		g := guard{n: v.n, op: opLength}
		if err := c.enter(g); err != nil {
			return 0, err
		}
		defer c.leave(g)
		return t.length(c)
	}
	return 0, nil
}

func (v *nodeValue) index(c *call, i int) (value, error) {
	cl, err := v.n.class()
	if err != nil {
		return nil, err
	}
	switch cl {
	case cArray:
		if err := v.expand(c); err != nil {
			return nil, err
		}
		if i < 0 || i >= len(v.elems) {
			return absent, nil
		}
		return v.elems[i], nil
	case cRef:
		t, err := v.ref(c)
		if err != nil {
			return nil, err
		}
		g := guard{n: v.n, op: opIndex, arg: strconv.Itoa(i)}
		if err := c.enter(g); err != nil {
			return nil, err
		}
		defer c.leave(g)
		return t.index(c, i)
	}
	return absent, nil
}

func (v *nodeValue) full(c *call) (any, error) {
	if v.done {
		return v.val, nil
	}
	cl, err := v.n.class()
	if err != nil {
		return nil, err
	}
	var val any
	switch cl {
	case cScalar:
		val = v.n.val
	case cArray:
		if err := v.expand(c); err != nil {
			return nil, err
		}
		out := make([]any, len(v.elems))
		for i, e := range v.elems {
			if out[i], err = e.full(c); err != nil {
				return nil, err
			}
		}
		val = out
	case cPlain:
		out := make(map[string]any, len(v.n.keys))
		for _, k := range v.n.keys {
			if err := c.checkKey(v.n, k); err != nil {
				return nil, err
			}
			if out[k], err = c.valueOf(v.n.obj[k]).full(c); err != nil {
				return nil, err
			}
		}
		val = out
	case cRef, cExtend:
		g := guard{n: v.n, op: opFull}
		if err := c.enter(g); err != nil {
			return nil, err
		}
		defer c.leave(g)
		var t value
		if cl == cRef {
			t, err = v.ref(c)
		} else {
			t, err = v.mergeValue(c)
		}
		if err != nil {
			return nil, err
		}
		if val, err = t.full(c); err != nil {
			return nil, err
		}
		if v.n.inDefs {
			if err := c.checkNoAnchor(val, v.n, "the value brought into $defs"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, spliceOutside(v.n)
	}
	v.val, v.done = val, true
	return val, nil
}

// ref returns the value a $ref node references.
func (v *nodeValue) ref(c *call) (value, error) {
	if v.target != nil {
		return v.target, nil
	}
	g := guard{n: v.n, op: opTarget}
	if err := c.enter(g); err != nil {
		return nil, err
	}
	defer c.leave(g)
	t, err := c.reference(v.n, v.n.obj["$ref"].val.(string))
	if err != nil {
		return nil, err
	}
	v.target = t
	return t, nil
}

func (v *nodeValue) mergeValue(c *call) (value, error) {
	m, err := v.merge(c)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// merge combines an $extend node's references, each resolved completely,
// and returns them merged with the node's own keys.
func (v *nodeValue) merge(c *call) (*mergeValue, error) {
	if v.merged != nil {
		return v.merged, nil
	}
	g := guard{n: v.n, op: opRefs}
	if err := c.enter(g); err != nil {
		return nil, err
	}
	defer c.leave(g)
	base := map[string]any{}
	for _, ref := range v.n.extendRefs() {
		t, err := c.reference(v.n, ref)
		if err != nil {
			return nil, err
		}
		x, err := t.full(c)
		if err != nil {
			return nil, err
		}
		switch x := x.(type) {
		case nil:
		case map[string]any:
			b := c.budget()
			if base = combine(base, x, b); b.exceeded() {
				return nil, b.err(v.n)
			}
		default:
			return nil, errorf(ErrExtendType, v.n, "%q resolves to %s", ref, describe(x))
		}
	}
	v.merged = &mergeValue{base: base, hasBase: true, patch: &patchObject{n: v.n, own: true}}
	return v.merged, nil
}

// expand splices an array's $splice elements, resolving each of their
// references completely, and leaves the other elements unresolved.
func (v *nodeValue) expand(c *call) error {
	if v.expanded {
		return nil
	}
	g := guard{n: v.n, op: opExpand}
	if err := c.enter(g); err != nil {
		return err
	}
	defer c.leave(g)
	elems := make([]value, 0, len(v.n.arr))
	for _, el := range v.n.arr {
		if el.kind != objectNode || el.obj["$splice"] == nil {
			elems = append(elems, c.valueOf(el))
			continue
		}
		if _, err := el.class(); err != nil {
			return err
		}
		refs, _ := el.refList("$splice")
		for _, ref := range refs {
			t, err := c.reference(el, ref)
			if err != nil {
				return err
			}
			x, err := t.full(c)
			if err != nil {
				return err
			}
			switch x := x.(type) {
			case nil:
			case []any:
				if el.inDefs {
					if err := c.checkNoAnchor(x, el, "an element spliced into $defs"); err != nil {
						return err
					}
				}
				for _, item := range x {
					elems = append(elems, plain{item})
				}
			default:
				return errorf(ErrSpliceType, el, "%q resolves to %s", ref, describe(x))
			}
		}
	}
	v.elems, v.expanded = elems, true
	return nil
}

// mergeValue merges a fully resolved base with a patch, following the
// $extend merge table: objects merge recursively, a deleting null removes
// the key, and any other patch value replaces the base.
//
// With keepDeletes, a deleted key is reported as vDelete rather than absent,
// so that the deletion also applies to an $extend further out. That is the
// case for an $extend nested in another's own keys.
type mergeValue struct {
	base        any
	hasBase     bool
	patch       value
	keepDeletes bool
	children    map[string]*mergeValue
	val         any
	done        bool
}

func (m *mergeValue) kind(c *call) (vkind, error) {
	pk, err := m.patch.kind(c)
	if err != nil {
		return 0, err
	}
	switch pk {
	case vDelete:
		if m.keepDeletes {
			return vDelete, nil
		}
		return vAbsent, nil
	case vAbsent:
		if m.hasBase {
			return plainKind(m.base), nil
		}
		return vAbsent, nil
	}
	return pk, nil
}

func (m *mergeValue) get(c *call, key string) (value, error) {
	pk, err := m.patch.kind(c)
	if err != nil {
		return nil, err
	}
	switch pk {
	case vObject:
		if ch, ok := m.children[key]; ok {
			return ch, nil
		}
		pc, err := m.patch.get(c, key)
		if err != nil {
			return nil, err
		}
		ch := &mergeValue{patch: pc, keepDeletes: m.keepDeletes}
		if b, ok := m.base.(map[string]any); ok && m.hasBase {
			ch.base, ch.hasBase = b[key]
		}
		if m.children == nil {
			m.children = map[string]*mergeValue{}
		}
		m.children[key] = ch
		return ch, nil
	case vAbsent:
		if m.hasBase {
			return plain{m.base}.get(c, key)
		}
		return absent, nil
	case vDelete:
		return absent, nil
	}
	return m.patch.get(c, key)
}

func (m *mergeValue) keys(c *call) ([]string, error) {
	pk, err := m.patch.kind(c)
	if err != nil {
		return nil, err
	}
	switch pk {
	case vObject:
		pks, err := m.patch.keys(c)
		if err != nil {
			return nil, err
		}
		set := map[string]bool{}
		for _, k := range pks {
			set[k] = true
		}
		if b, ok := m.base.(map[string]any); ok && m.hasBase {
			for k := range b {
				set[k] = true
			}
		}
		var out []string
		for _, k := range slices.Sorted(maps.Keys(set)) {
			ch, err := m.get(c, k)
			if err != nil {
				return nil, err
			}
			ck, err := ch.kind(c)
			if err != nil {
				return nil, err
			}
			if ck != vAbsent {
				out = append(out, k)
			}
		}
		return out, nil
	case vAbsent:
		if m.hasBase {
			return plain{m.base}.keys(c)
		}
		return nil, nil
	case vDelete:
		return nil, nil
	}
	return m.patch.keys(c)
}

func (m *mergeValue) length(c *call) (int, error) {
	pk, err := m.patch.kind(c)
	if err != nil {
		return 0, err
	}
	if pk == vAbsent && m.hasBase {
		return plain{m.base}.length(c)
	}
	return m.patch.length(c)
}

func (m *mergeValue) index(c *call, i int) (value, error) {
	pk, err := m.patch.kind(c)
	if err != nil {
		return nil, err
	}
	if pk == vAbsent && m.hasBase {
		return plain{m.base}.index(c, i)
	}
	return m.patch.index(c, i)
}

func (m *mergeValue) full(c *call) (any, error) {
	if m.done {
		return m.val, nil
	}
	pk, err := m.patch.kind(c)
	if err != nil {
		return nil, err
	}
	var val any
	switch pk {
	case vObject:
		ks, err := m.keys(c)
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, len(ks))
		for _, k := range ks {
			ch, err := m.get(c, k)
			if err != nil {
				return nil, err
			}
			if ck, err := ch.kind(c); err != nil {
				return nil, err
			} else if ck == vDelete {
				continue
			}
			if out[k], err = ch.full(c); err != nil {
				return nil, err
			}
		}
		val = out
	case vAbsent:
		val = m.base
	case vDelete:
	default:
		if val, err = m.patch.full(c); err != nil {
			return nil, err
		}
	}
	m.val, m.done = val, true
	return val, nil
}

// patchObject is a plain object at a patch position: its own keys merge over
// the base key by key, and a null written in it deletes. For an $extend
// node's own keys, own is set and the $extend key itself is left out.
type patchObject struct {
	n   *node
	own bool
}

func (p *patchObject) skip(k string) bool { return p.own && (k == "$extend" || k == "$extends") }

func (p *patchObject) kind(*call) (vkind, error) { return vObject, nil }

func (p *patchObject) get(c *call, key string) (value, error) {
	if p.skip(key) {
		return absent, nil
	}
	if err := c.checkKey(p.n, key); err != nil {
		return nil, err
	}
	ch, ok := p.n.obj[key]
	if !ok {
		return absent, nil
	}
	return c.patchOf(ch)
}

func (p *patchObject) keys(c *call) ([]string, error) {
	out := make([]string, 0, len(p.n.keys))
	for _, k := range p.n.keys {
		if p.skip(k) {
			continue
		}
		if err := c.checkKey(p.n, k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func (p *patchObject) length(*call) (int, error)       { return 0, nil }
func (p *patchObject) index(*call, int) (value, error) { return absent, nil }
func (p *patchObject) full(c *call) (any, error)       { return (&mergeValue{patch: p}).full(c) }

// extendPatch is an $extend node nested in another $extend's own keys, as a
// patch: its references merged with its own keys, keeping the nulls written
// there as deletions, so they also delete from the $extend further out.
type extendPatch struct {
	v *nodeValue
	m *mergeValue
}

func (e *extendPatch) merged(c *call) (*mergeValue, error) {
	if e.m == nil {
		inner, err := e.v.merge(c)
		if err != nil {
			return nil, err
		}
		e.m = &mergeValue{base: inner.base, hasBase: true, patch: inner.patch, keepDeletes: true}
	}
	return e.m, nil
}

func (e *extendPatch) kind(*call) (vkind, error) { return vObject, nil }

func (e *extendPatch) get(c *call, key string) (value, error) {
	m, err := e.merged(c)
	if err != nil {
		return nil, err
	}
	return m.get(c, key)
}

func (e *extendPatch) keys(c *call) ([]string, error) {
	m, err := e.merged(c)
	if err != nil {
		return nil, err
	}
	return m.keys(c)
}

func (e *extendPatch) length(*call) (int, error)       { return 0, nil }
func (e *extendPatch) index(*call, int) (value, error) { return absent, nil }
func (e *extendPatch) full(c *call) (any, error)       { return e.v.full(c) }

// stripped is a value referenced from another document, which never
// carries a $schema.
type stripped struct {
	inner value
	val   any
	done  bool
}

func strip(v value) value {
	switch v.(type) {
	case marker, *stripped:
		return v
	}
	return &stripped{inner: v}
}

func (s *stripped) kind(c *call) (vkind, error) { return s.inner.kind(c) }

func (s *stripped) get(c *call, key string) (value, error) {
	if key == "$schema" {
		return absent, nil
	}
	ch, err := s.inner.get(c, key)
	if err != nil {
		return nil, err
	}
	return strip(ch), nil
}

func (s *stripped) keys(c *call) ([]string, error) {
	ks, err := s.inner.keys(c)
	if err != nil || !slices.Contains(ks, "$schema") {
		return ks, err
	}
	return slices.DeleteFunc(slices.Clone(ks), func(k string) bool { return k == "$schema" }), nil
}

func (s *stripped) length(c *call) (int, error) { return s.inner.length(c) }

func (s *stripped) index(c *call, i int) (value, error) {
	ch, err := s.inner.index(c, i)
	if err != nil {
		return nil, err
	}
	return strip(ch), nil
}

func (s *stripped) full(c *call) (any, error) {
	if s.done {
		return s.val, nil
	}
	x, err := s.inner.full(c)
	if err != nil {
		return nil, err
	}
	b := c.budget()
	if x = withoutSchema(x, b); b.exceeded() {
		return nil, &Error{Kind: ErrLimit, Detail: fmt.Sprintf("a referenced value holds more than %d values", b.max)}
	}
	s.val, s.done = x, true
	return s.val, nil
}

// withoutSchema returns a copy of v with every $schema key removed.
func withoutSchema(v any, b *budget) any {
	if !b.spend() {
		return nil
	}
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if k == "$schema" {
				continue
			}
			if out[k] = withoutSchema(x, b); b.exceeded() {
				return nil
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			if out[i] = withoutSchema(x, b); b.exceeded() {
				return nil
			}
		}
		return out
	}
	return v
}

// combine merges y over x for several $extend references: objects merge
// recursively, and any other value from y, null included, replaces x's.
// Neither argument is modified.
func combine(x, y map[string]any, b *budget) map[string]any {
	out := maps.Clone(x)
	for k, yv := range y {
		if !b.spend() {
			return nil
		}
		xm, ok1 := out[k].(map[string]any)
		ym, ok2 := yv.(map[string]any)
		if ok1 && ok2 {
			if out[k] = combine(xm, ym, b); b.exceeded() {
				return nil
			}
		} else {
			out[k] = yv
		}
	}
	return out
}

func describe(v any) string {
	switch v.(type) {
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	}
	return "a number"
}

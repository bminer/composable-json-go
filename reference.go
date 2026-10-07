package composablejson

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// class is what kind of node a node is, once its directives are checked.
type class uint8

const (
	cScalar class = iota
	cArray
	cPlain
	cRef
	cExtend
	cSplice
)

// class classifies n, checking its directives' structure but not following
// any reference. The result depends only on n, so it is kept on the node.
func (n *node) class() (class, error) {
	switch n.kind {
	case scalarNode:
		return cScalar, nil
	case arrayNode:
		return cArray, nil
	}
	if !n.classified {
		n.cls, n.clsErr = n.classify()
		n.classified = true
	}
	return n.cls, n.clsErr
}

func (n *node) classify() (class, error) {
	ref, hasRef := n.obj["$ref"]
	_, hasSplice := n.obj["$splice"]
	_, hasExtend := n.obj["$extend"]
	_, hasExtends := n.obj["$extends"]
	switch {
	case hasRef:
		if err := n.checkSiblings("$ref"); err != nil {
			return 0, err
		}
		if _, ok := ref.val.(string); !ok {
			return 0, errorf(ErrMalformedDirective, ref, "$ref must be a reference string")
		}
		return cRef, nil
	case hasSplice:
		if err := n.checkSiblings("$splice"); err != nil {
			return 0, err
		}
		if _, err := n.refList("$splice"); err != nil {
			return 0, err
		}
		return cSplice, nil
	case hasExtend && hasExtends:
		return 0, errorf(ErrExtendSynonym, n, "use $extend or $extends, not both")
	case hasExtend || hasExtends:
		if _, err := n.refList(n.extendKey()); err != nil {
			return 0, err
		}
		return cExtend, nil
	}
	return cPlain, nil
}

// checkSiblings reports an error for any key of n beside directive other
// than a string $comment, which explains the reference and is dropped with
// the node.
func (n *node) checkSiblings(directive string) error {
	var others []string
	for _, k := range n.keys {
		if k != directive && k != "$comment" {
			others = append(others, strconv.Quote(k))
		}
	}
	if len(others) > 0 {
		return errorf(ErrSiblingKeys, n, "%s allows no other key but $comment, but the object also has %s", directive, strings.Join(others, ", "))
	}
	if c, ok := n.obj["$comment"]; ok {
		if _, ok := c.val.(string); !ok {
			return errorf(ErrMalformedDirective, c, "$comment must be a string")
		}
	}
	return nil
}

func (n *node) extendKey() string {
	if _, ok := n.obj["$extend"]; ok {
		return "$extend"
	}
	return "$extends"
}

func (n *node) extendRefs() []string {
	refs, _ := n.refList(n.extendKey())
	return refs
}

// refList returns the references of n's $extend or $splice key, which must be
// a reference string or a non-empty array of them.
func (n *node) refList(key string) ([]string, error) {
	v := n.obj[key]
	switch v.kind {
	case scalarNode:
		if s, ok := v.val.(string); ok {
			return []string{s}, nil
		}
	case arrayNode:
		if len(v.arr) == 0 {
			return nil, errorf(ErrEmptyReferences, v, "%s lists no references", key)
		}
		refs := make([]string, len(v.arr))
		for i, e := range v.arr {
			s, ok := e.val.(string)
			if !ok {
				return nil, errorf(ErrMalformedDirective, e, "each entry of %s must be a reference string", key)
			}
			refs[i] = s
		}
		return refs, nil
	}
	return nil, errorf(ErrMalformedDirective, v, "%s must be a reference string or an array of them", key)
}

func spliceOutside(n *node) error {
	return errorf(ErrSplicePosition, n, "$splice is allowed only on an element of an array")
}

// A target is where a reference points: another document, or the containing
// one when uri is nil, and a fragment within it.
type target struct {
	uri  *url.URL
	frag Fragment
}

// target parses ref, made from within from's document, and checks that it
// may be followed. It records the dependency on any other document.
func (c *call) target(from *node, ref string) (target, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return target{}, &Error{Kind: ErrMalformedDirective, Location: from.loc(), Detail: fmt.Sprintf("%q is not a valid URI reference", ref), Err: err}
	}
	frag, err := parseFragment(u.Fragment)
	if err != nil {
		e := err.(*Error)
		e.Location = from.loc()
		return target{}, e
	}
	d := from.doc
	if fragmentOnly(u) {
		return target{frag: frag}, nil
	}
	var abs *url.URL
	switch {
	case u.IsAbs():
		cp := *u
		abs = &cp
	case d.uri == nil:
		return target{}, errorf(ErrMissingBaseURI, from, "%q is relative, but the document has no base URI", ref)
	default:
		abs = d.uri.ResolveReference(u)
	}
	abs.Fragment, abs.RawFragment = "", ""
	key := abs.String()
	if key == d.key {
		return target{frag: frag}, nil
	}
	tg := c.r.group(abs)
	if c.local != nil && key == c.local.key {
		tg = c.local.group
	}
	if err := c.r.checkMove(d.group, d.key, abs, tg, from); err != nil {
		return target{}, err
	}
	d.deps[key] = true
	return target{uri: abs, frag: frag}, nil
}

func fragmentOnly(u *url.URL) bool {
	return u.Scheme == "" && u.Opaque == "" && u.User == nil && u.Host == "" && u.Path == "" &&
		u.RawQuery == "" && !u.ForceQuery
}

// reference returns the value ref, made from within from's document, selects.
func (c *call) reference(from *node, ref string) (value, error) {
	t, err := c.target(from, ref)
	if err != nil {
		return nil, err
	}
	d := from.doc
	if t.uri != nil {
		if d, err = c.load(t.uri, from); err != nil {
			return nil, err
		}
	}
	var v value
	if t.frag.Anchor != "" {
		if v, err = c.anchor(d, t.frag.Anchor, from); err != nil {
			return nil, err
		}
	} else {
		v = c.valueOf(d.root)
	}
	v, found, err := c.walk(v, t.frag.Pointer)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errorf(ErrUnresolvable, from, "%q selects nothing", ref)
	}
	// A reference never delivers $schema, whichever document it points into.
	return strip(v), nil
}

// walk follows p from v through resolved values, resolving only what each
// step needs. It reports whether anything is there.
func (c *call) walk(v value, p Pointer) (value, bool, error) {
	for _, tok := range p {
		k, err := v.kind(c)
		if err != nil {
			return nil, false, err
		}
		next := absent
		switch k {
		case vObject:
			if next, err = v.get(c, tok); err != nil {
				return nil, false, err
			}
		case vArray:
			if i, ok := arrayIndex(tok); ok {
				if next, err = v.index(c, i); err != nil {
					return nil, false, err
				}
			}
		}
		nk, err := next.kind(c)
		if err != nil {
			return nil, false, err
		}
		if nk == vAbsent || nk == vDelete {
			return nil, false, nil
		}
		v = next
	}
	return v, true, nil
}

// arrayIndex parses an RFC 6901 array index: digits, with no leading zero.
func arrayIndex(tok string) (int, bool) {
	if tok == "" || len(tok) > 1 && tok[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return 0, false
		}
	}
	i, err := strconv.Atoi(tok)
	return i, err == nil
}

// load returns the document at u, loading and caching it if need be.
func (c *call) load(u *url.URL, from *node) (*document, error) {
	key := u.String()
	if c.local != nil && key == c.local.key {
		return c.local, nil
	}
	if d, ok := c.r.docs[key]; ok {
		return d, nil
	}
	at := Location{URI: key}
	if from != nil {
		at = from.loc()
	}
	fail := func(kind error, detail string, err error) error {
		return &Error{Kind: kind, Location: at, Detail: detail, Err: err}
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)
	loader, g := c.r.loaders[scheme], c.r.groups[scheme]
	if loader == nil {
		return nil, fail(ErrUnresolvable, fmt.Sprintf("no loader is registered for scheme %q", scheme), nil)
	}
	if c.loaded++; c.loaded > c.r.limits.MaxDocuments {
		return nil, fail(ErrLimit, fmt.Sprintf("the call loads more than %d documents", c.r.limits.MaxDocuments), nil)
	}
	res, err := loader.Load(context.WithValue(c.ctx, resolverKey{}, c.r), u)
	if err != nil {
		// A redirect refused by CheckRedirect keeps its own kind.
		if e := (*Error)(nil); errors.As(err, &e) {
			cp := *e
			cp.Location = at
			return nil, &cp
		}
		return nil, fail(ErrUnresolvable, "cannot load "+key, err)
	}
	if res == nil || res.Body == nil {
		return nil, fail(ErrUnresolvable, "the loader returned no content for "+key, nil)
	}
	defer res.Body.Close()

	// A loader that followed a redirect reports where it ended up, which
	// must obey the same rules as a reference, and becomes the base URI.
	// The document is still cached under the URI that was requested.
	final := u
	if res.URI != nil && res.URI.String() != key {
		f := *res.URI
		if !f.IsAbs() || f.Fragment != "" {
			return nil, fail(ErrUnresolvable, fmt.Sprintf("the loader reported %s, which is not an absolute URI without a fragment", &f), nil)
		}
		if err := c.r.checkMove(g, key, &f, c.r.group(&f), from); err != nil {
			return nil, err
		}
		final, g = &f, c.r.group(&f)
	}
	max := c.r.limits.MaxBytes
	d, err := newDocument(final, g, &limitedBody{r: res.Body, key: final.String(), left: max, max: max})
	if err != nil {
		return nil, err
	}
	c.r.docs[key] = d
	return d, nil
}

package composablejson

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Options configures a [Resolver].
type Options struct {
	// Local holds the loaders for schemes that read from this machine, by
	// scheme. Nil means {"file": FileLoader{}}; any other value, including an
	// empty map, replaces that default rather than adding to it.
	Local map[string]Loader
	// Remote holds the loaders for schemes that fetch over a network, by
	// scheme. Nil means none, so remote references are errors. A document
	// loaded through one of these may not reference a Local scheme.
	Remote map[string]Loader
	// HostDirectives lists the $-prefixed keys the host format defines, such
	// as "$csv". The key is left in the output for the host, and its value is
	// resolved like any other. A name the specification defines, or $id, is
	// an error.
	HostDirectives []string
}

// A Resolver resolves documents, caching each document it loads, and the
// values resolved within it, across calls. A document referenced by many
// others is therefore read, parsed and resolved once.
//
// A Resolver is safe for concurrent use, though it runs one resolution at a
// time. Call [Resolver.Reset] when documents change.
type Resolver struct {
	local  map[string]Loader
	remote map[string]Loader
	host   map[string]bool
	err    error // the Options were invalid

	mu   sync.Mutex
	docs map[string]*document // documents loaded so far, by URI
}

// specKeys are the keys the specification defines or reserves, which a host
// format may not take.
var specKeys = map[string]bool{
	"$ref": true, "$extend": true, "$extends": true, "$splice": true, "$anchor": true,
	"$defs": true, "$comment": true, "$schema": true, "$id": true,
}

// NewResolver returns a Resolver configured by opts. Invalid options are
// reported by each call to Resolve or ResolveBytes.
func NewResolver(opts Options) *Resolver {
	r := &Resolver{
		local:  lowerKeys(opts.Local),
		remote: lowerKeys(opts.Remote),
		host:   map[string]bool{},
		docs:   map[string]*document{},
	}
	if opts.Local == nil {
		r.local = map[string]Loader{"file": FileLoader{}}
	}
	for _, s := range slices.Sorted(maps.Keys(r.local)) {
		if r.remote[s] != nil {
			r.err = fmt.Errorf("composablejson: scheme %q is registered as both local and remote", s)
		}
	}
	for s, l := range r.local {
		if l == nil {
			r.err = fmt.Errorf("composablejson: scheme %q has a nil loader", s)
		}
	}
	for s, l := range r.remote {
		if l == nil {
			r.err = fmt.Errorf("composablejson: scheme %q has a nil loader", s)
		}
	}
	for _, h := range opts.HostDirectives {
		if !strings.HasPrefix(h, "$") || specKeys[h] {
			r.err = fmt.Errorf("composablejson: %q cannot be a host directive", h)
		}
		r.host[h] = true
	}
	return r
}

func lowerKeys(m map[string]Loader) map[string]Loader {
	out := make(map[string]Loader, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// Resolve loads the document at uri, which must be absolute and have no
// fragment, and resolves it.
func (r *Resolver) Resolve(ctx context.Context, uri *url.URL) (*Document, error) {
	if r.err != nil {
		return nil, r.err
	}
	u, err := documentURI(uri)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.newCall(ctx)
	d, err := c.load(u, nil)
	if err != nil {
		return nil, err
	}
	return c.resolveRoot(d)
}

// ResolveBytes resolves a document held in memory. base is the document's
// URI, against which its relative references resolve. With a nil base, only
// fragment-only and absolute references work.
//
// The document itself is never cached, and neither is anything resolved
// from it, so a later call with the same base and different data is
// unaffected. Within the call, the document stands in for base: a loaded
// document that references base sees data, not what base would load.
func (r *Resolver) ResolveBytes(ctx context.Context, data []byte, base *url.URL) (*Document, error) {
	if r.err != nil {
		return nil, r.err
	}
	var u *url.URL
	if base != nil {
		var err error
		if u, err = documentURI(base); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	remote := u != nil && r.local[strings.ToLower(u.Scheme)] == nil
	d, err := newDocument(u, remote, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c := r.newCall(ctx)
	c.local = d
	if u != nil {
		// Values cached from documents that reference base were resolved
		// against what base loads, not against data, so set them aside both
		// before the call and, for anything that came to depend on data,
		// after it.
		r.evictDependents(d.key)
		defer r.evictDependents(d.key)
	}
	return c.resolveRoot(d)
}

// Reset discards every cached document and value.
func (r *Resolver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = map[string]*document{}
}

// Resolve resolves the document at uri with a new [Resolver].
func Resolve(ctx context.Context, uri *url.URL, opts Options) (*Document, error) {
	return NewResolver(opts).Resolve(ctx, uri)
}

// ResolveBytes resolves a document held in memory with a new [Resolver].
func ResolveBytes(ctx context.Context, data []byte, base *url.URL, opts Options) (*Document, error) {
	return NewResolver(opts).ResolveBytes(ctx, data, base)
}

func documentURI(uri *url.URL) (*url.URL, error) {
	if uri == nil || !uri.IsAbs() || uri.Fragment != "" {
		return nil, &Error{Kind: ErrUnresolvable, Detail: fmt.Sprintf("%v must be an absolute URI without a fragment", uri)}
	}
	u := *uri
	if u.User != nil {
		user := *u.User
		u.User = &user
	}
	return &u, nil
}

// evictDependents discards the cached values of the document at key and of
// every document that references it, directly or not. Their parsed trees
// are kept.
func (r *Resolver) evictDependents(key string) {
	hit := map[string]bool{key: true}
	for changed := true; changed; {
		changed = false
		for k, d := range r.docs {
			if hit[k] {
				continue
			}
			for dep := range d.deps {
				if hit[dep] {
					hit[k], changed = true, true
					break
				}
			}
		}
	}
	for k := range hit {
		if d := r.docs[k]; d != nil {
			d.lazy = nil
		}
	}
}

// docState holds a document's lazily resolved values.
type docState struct {
	values  map[*node]*nodeValue
	patches map[*node]value
	imports map[string][]candidate
}

// A call is one resolution: the state that must not outlive it.
type call struct {
	r      *Resolver
	ctx    context.Context
	local  *document // the ResolveBytes document, which is never cached
	active map[guard]int
	stack  []guard
}

func (r *Resolver) newCall(ctx context.Context) *call {
	return &call{r: r, ctx: ctx, active: map[guard]int{}}
}

func (c *call) state(d *document) *docState {
	if d.lazy == nil {
		d.lazy = &docState{
			values:  map[*node]*nodeValue{},
			patches: map[*node]value{},
		}
	}
	return d.lazy
}

// valueOf returns the value of n on its own.
func (c *call) valueOf(n *node) *nodeValue {
	st := c.state(n.doc)
	if v, ok := st.values[n]; ok {
		return v
	}
	v := &nodeValue{n: n}
	st.values[n] = v
	return v
}

// patchOf returns n as a patch: what a node under an $extend's own keys
// merges over the base. A null written there deletes, and so does one written
// under a nested $extend.
func (c *call) patchOf(n *node) (value, error) {
	if n.kind == scalarNode && n.val == nil {
		return deleted, nil
	}
	if n.kind != objectNode {
		return c.valueOf(n), nil
	}
	cl, err := n.class()
	if err != nil {
		return nil, err
	}
	st := c.state(n.doc)
	if p, ok := st.patches[n]; ok {
		return p, nil
	}
	var p value
	switch cl {
	case cPlain:
		p = &patchObject{n: n}
	case cExtend:
		p = &extendPatch{v: c.valueOf(n)}
	default:
		return c.valueOf(n), nil
	}
	st.patches[n] = p
	return p, nil
}

// position returns the value at n's position in its resolved document, which
// for a node under an $extend's own keys includes what the $extend merges in.
func (c *call) position(n *node) (value, error) {
	var path []string
	x := n
	for ; x.patch; x = x.parent {
		path = append(path, x.key)
	}
	var v value = c.valueOf(x)
	for i := len(path) - 1; i >= 0; i-- {
		var err error
		if v, err = v.get(c, path[i]); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// checkKey reports an error for a $-prefixed key of n that is unknown or
// whose value is malformed. Other keys, and keys n lacks, are fine.
func (c *call) checkKey(n *node, k string) error {
	ch := n.obj[k]
	if ch == nil || !strings.HasPrefix(k, "$") || c.r.host[k] {
		return nil
	}
	switch k {
	case "$anchor":
		if s, ok := ch.val.(string); !ok || !validAnchor(s) {
			return errorf(ErrMalformedDirective, ch, "$anchor must be a name matching ^[A-Za-z_][-A-Za-z0-9._]*$")
		}
		if n.inDefs {
			return errorf(ErrAnchorInDefs, n, "$anchor is not allowed inside $defs")
		}
	case "$comment", "$schema":
		if _, ok := ch.val.(string); !ok {
			return errorf(ErrMalformedDirective, ch, "%s must be a string", k)
		}
	case "$defs":
		if ch.kind != objectNode {
			return errorf(ErrMalformedDirective, ch, "$defs must be an object")
		}
	case "$id", "$ref", "$extend", "$extends", "$splice":
	default:
		return errorf(ErrUnknownDirective, ch, "%q is defined by neither the specification nor the host format", k)
	}
	return nil
}

// guard identifies an operation in progress, so that one needed again before
// it finishes is reported as a cycle.
type guard struct {
	n   *node
	d   *document
	op  op
	arg string
}

type op uint8

const (
	opKind op = iota
	opGet
	opKeys
	opLength
	opIndex
	opFull
	opTarget
	opRefs
	opExpand
	opImports
)

func (g guard) loc() Location {
	if g.n != nil {
		return g.n.loc()
	}
	return Location{URI: g.d.key}
}

func (c *call) enter(g guard) error {
	if i, ok := c.active[g]; ok {
		var chain []Location
		for _, h := range append(slices.Clone(c.stack[i:]), g) {
			l := h.loc()
			if n := len(chain); n == 0 || chain[n-1].String() != l.String() {
				chain = append(chain, l)
			}
		}
		return &Error{Kind: ErrCycle, Location: g.loc(), Detail: "the node is needed while it is being resolved", Chain: chain}
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.active[g] = len(c.stack)
	c.stack = append(c.stack, g)
	return nil
}

func (c *call) leave(g guard) {
	delete(c.active, g)
	c.stack = c.stack[:len(c.stack)-1]
}

// resolveRoot resolves d completely and checks that its anchors are unique.
func (c *call) resolveRoot(d *document) (*Document, error) {
	v, err := c.valueOf(d.root).full(c)
	if err != nil {
		return nil, err
	}
	anchors := map[string]Pointer{}
	var dup *Error
	scanAnchors(v, nil, func(name string, p Pointer) bool {
		prev, ok := anchors[name]
		if !ok {
			anchors[name] = p
			return true
		}
		detail := fmt.Sprintf("%q names two nodes", name)
		if len(d.written[name]) < 2 {
			detail += "; at least one was imported or copied by a reference"
		}
		dup = &Error{
			Kind:     ErrDuplicateAnchor,
			Location: Location{URI: d.key, Pointer: p},
			Detail:   detail,
			Chain:    []Location{{URI: d.key, Pointer: prev}, {URI: d.key, Pointer: p}},
		}
		return false
	})
	if dup != nil {
		return nil, dup
	}
	doc := &Document{Value: deepCopy(v), anchors: anchors}
	if d.uri != nil {
		u := *d.uri
		doc.URI = &u
	}
	return doc, nil
}

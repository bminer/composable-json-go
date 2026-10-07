package composablejson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
)

// maxDepth bounds how deeply a document may nest, so that hostile input
// cannot exhaust the stack.
const maxDepth = 10000

type nodeKind uint8

const (
	scalarNode nodeKind = iota
	objectNode
	arrayNode
)

// A node is one value in a document as written, before resolution.
type node struct {
	doc    *document
	parent *node
	key    string // key within the parent object
	index  int    // index within the parent array, or -1
	kind   nodeKind
	keys   []string // object keys, in source order
	obj    map[string]*node
	arr    []*node
	val    any // scalar: string, json.Number, bool or nil

	// patch is set for a node whose value is merged over an $extend's
	// references: a key of an $extend node's own keys, or a key of a plain
	// object that is itself one. A null written there deletes.
	patch bool

	classified bool
	cls        class
	clsErr     error
}

// pointer returns n's position within its document.
func (n *node) pointer() Pointer {
	depth := 0
	for x := n; x.parent != nil; x = x.parent {
		depth++
	}
	p := make(Pointer, depth)
	for x := n; x.parent != nil; x = x.parent {
		depth--
		if x.index >= 0 {
			p[depth] = strconv.Itoa(x.index)
		} else {
			p[depth] = x.key
		}
	}
	return p
}

func (n *node) loc() Location { return Location{URI: n.doc.key, Pointer: n.pointer()} }

// A document is one parsed JSON document. Its tree never changes; the lazily
// resolved values in lazy are cached separately, so they can be discarded.
type document struct {
	uri   *url.URL // nil for a document with no base URI
	key   string   // uri.String(), or ""
	group group
	root  *node

	written    map[string][]*node // nodes declaring each valid $anchor name
	directives []*node            // nodes with $ref, $extend(s) or $splice, in source order
	deps       map[string]bool    // documents this one has referenced, by URI

	lazy *docState
}

func newDocument(u *url.URL, g group, rd io.Reader) (*document, error) {
	d := &document{uri: u, group: g, written: map[string][]*node{}, deps: map[string]bool{}}
	if u != nil {
		d.key = u.String()
	}
	root, err := parse(d, rd)
	if err != nil {
		return nil, err
	}
	d.root = root
	d.annotate(root)
	return d, nil
}

// annotate records written anchors and directives, and sets each node's
// patch flag.
func (d *document) annotate(n *node) {
	switch n.kind {
	case objectNode:
		_, ref := n.obj["$ref"]
		_, splice := n.obj["$splice"]
		_, ext := n.obj["$extend"]
		_, exts := n.obj["$extends"]
		extend := ext || exts
		plain := !ref && !splice && !extend
		if a, ok := n.obj["$anchor"]; ok {
			if s, ok := a.val.(string); ok && validAnchor(s) {
				d.written[s] = append(d.written[s], n)
			}
		}
		if !plain {
			d.directives = append(d.directives, n)
		}
		for _, k := range n.keys {
			c := n.obj[k]
			c.patch = extend && k != "$extend" && k != "$extends" || n.patch && plain
			d.annotate(c)
		}
	case arrayNode:
		for _, c := range n.arr {
			d.annotate(c)
		}
	}
}

type parser struct {
	dec   *json.Decoder
	rd    *errReader
	doc   *document
	depth int
}

// errReader remembers a read error, so that it is not mistaken for invalid
// JSON.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

func parse(d *document, r io.Reader) (*node, error) {
	rd := &errReader{r: r}
	dec := json.NewDecoder(rd)
	dec.UseNumber()
	p := &parser{dec: dec, rd: rd, doc: d}
	root, err := p.value(nil, "", -1)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			err = errors.New("unexpected data after the top-level value")
		}
		return nil, p.fail(err)
	}
	return root, nil
}

func (p *parser) fail(err error) error {
	if p.rd.err != nil {
		if e, ok := p.rd.err.(*Error); ok {
			return e // the document is too large
		}
		return &Error{Kind: ErrUnresolvable, Location: Location{URI: p.doc.key}, Detail: "cannot read the document", Err: p.rd.err}
	}
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return &Error{Kind: ErrInvalidJSON, Location: Location{URI: p.doc.key}, Err: err}
}

func (p *parser) value(parent *node, key string, index int) (*node, error) {
	tok, err := p.dec.Token()
	if err != nil {
		return nil, p.fail(err)
	}
	n := &node{doc: p.doc, parent: parent, key: key, index: index}
	delim, ok := tok.(json.Delim)
	if !ok {
		n.val = tok
		return n, nil
	}
	if p.depth++; p.depth > maxDepth {
		return nil, p.fail(fmt.Errorf("nesting exceeds %d levels", maxDepth))
	}
	defer func() { p.depth-- }()
	switch delim {
	case '{':
		n.kind = objectNode
		n.obj = map[string]*node{}
		for p.dec.More() {
			t, err := p.dec.Token()
			if err != nil {
				return nil, p.fail(err)
			}
			k, _ := t.(string)
			if _, dup := n.obj[k]; dup {
				return nil, errorf(ErrDuplicateKey, n, "key %q appears more than once", k)
			}
			c, err := p.value(n, k, -1)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k)
			n.obj[k] = c
		}
	case '[':
		n.kind = arrayNode
		for p.dec.More() {
			c, err := p.value(n, "", len(n.arr))
			if err != nil {
				return nil, err
			}
			n.arr = append(n.arr, c)
		}
	default:
		return nil, p.fail(fmt.Errorf("unexpected %q", rune(delim)))
	}
	if _, err := p.dec.Token(); err != nil {
		return nil, p.fail(err)
	}
	return n, nil
}

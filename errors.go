package composablejson

import (
	"errors"
	"fmt"
	"strings"
)

// Error kinds, one for each error in the specification. Every error that
// resolution reports is an [*Error] whose Kind is one of these, so
// errors.Is(err, ErrCycle) and the like identify it.
var (
	// ErrInvalidJSON reports a document that is not valid JSON.
	ErrInvalidJSON = errors.New("invalid JSON")
	// ErrDuplicateKey reports an object with the same key twice.
	ErrDuplicateKey = errors.New("duplicate key")
	// ErrUnresolvable reports a reference that cannot be resolved: a document
	// that cannot be loaded, a scheme with no loader, or a fragment that
	// selects nothing.
	ErrUnresolvable = errors.New("unresolvable reference")
	// ErrMissingBaseURI reports a relative reference in a document that has
	// no base URI.
	ErrMissingBaseURI = errors.New("relative reference without a base URI")
	// ErrRemoteToLocal reports a remote document that references, or is
	// redirected to, a local resource.
	ErrRemoteToLocal = errors.New("remote document references a local resource")
	// ErrInsecureReference reports a document retrieved with protection in
	// transit that references, or is redirected to, a resource retrieved
	// without it: an Options.Remote document referencing an
	// Options.Insecure one.
	ErrInsecureReference = errors.New("secure document references an insecure resource")
	// ErrCycle reports a node that is needed while it is still being
	// resolved.
	ErrCycle = errors.New("reference cycle")
	// ErrDuplicateAnchor reports two nodes carrying the same $anchor.
	ErrDuplicateAnchor = errors.New("duplicate $anchor")
	// ErrAnchorInDefs reports an $anchor inside $defs.
	ErrAnchorInDefs = errors.New("$anchor inside $defs")
	// ErrExtendType reports an $extend reference to a value that is neither
	// an object nor null.
	ErrExtendType = errors.New("$extend target is neither an object nor null")
	// ErrSpliceType reports a $splice reference to a value that is neither
	// an array nor null.
	ErrSpliceType = errors.New("$splice target is neither an array nor null")
	// ErrSplicePosition reports $splice on an object that is not an element
	// of an array.
	ErrSplicePosition = errors.New("$splice outside an array")
	// ErrSiblingKeys reports a key alongside $ref or $splice.
	ErrSiblingKeys = errors.New("key alongside $ref or $splice")
	// ErrExtendSynonym reports a node with both $extend and $extends.
	ErrExtendSynonym = errors.New("both $extend and $extends")
	// ErrEmptyReferences reports $extend or $splice given an empty array.
	ErrEmptyReferences = errors.New("empty list of references")
	// ErrMalformedDirective reports a directive whose value is malformed or
	// of the wrong type, including a reference with invalid syntax.
	ErrMalformedDirective = errors.New("malformed directive")
	// ErrUnknownDirective reports a $-prefixed key defined by neither the
	// specification nor the host format.
	ErrUnknownDirective = errors.New("unknown directive")
	// ErrLimit reports that resolution exceeded one of its [Limits]. The
	// specification leaves limits to implementations, so it lists no such
	// error.
	ErrLimit = errors.New("limit exceeded")
)

// Error describes a failure to resolve a document.
type Error struct {
	// Kind is one of the Err variables.
	Kind error
	// Location is the offending node.
	Location Location
	// Detail explains the failure.
	Detail string
	// Chain lists, for a cycle, every node in it, and for a duplicate
	// anchor, both nodes that carry it.
	Chain []Location
	// Err is the underlying cause, if any, such as a loader's error.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("composablejson: ")
	if loc := e.Location.String(); loc != "" {
		b.WriteString(loc)
		b.WriteString(": ")
	}
	b.WriteString(e.Kind.Error())
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if len(e.Chain) > 0 {
		sep := ", "
		if e.Kind == ErrCycle {
			sep = " -> "
		}
		b.WriteString(" (")
		for i, l := range e.Chain {
			if i > 0 {
				b.WriteString(sep)
			}
			b.WriteString(l.String())
		}
		b.WriteString(")")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Is reports whether target is e's Kind.
func (e *Error) Is(target error) bool { return target == e.Kind }

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// Location identifies a node: the document it is in, and its JSON Pointer
// within that document as written, before resolution.
type Location struct {
	// URI is the document's URI, or "" for a document with no base URI.
	URI string
	// Pointer is the node's position within the document.
	Pointer Pointer
}

// String formats l as a URI reference, such as "file:///srv/a.json#/p".
func (l Location) String() string {
	if len(l.Pointer) == 0 {
		return l.URI
	}
	return l.URI + "#" + Fragment{Pointer: l.Pointer}.String()
}

func errorf(kind error, n *node, format string, args ...any) *Error {
	e := &Error{Kind: kind, Detail: fmt.Sprintf(format, args...)}
	if n != nil {
		e.Location = n.loc()
	}
	return e
}

package composablejson

import (
	"net/url"
	"strconv"
	"strings"
)

// Pointer is a parsed JSON Pointer (RFC 6901), with one unescaped token per
// step. The empty Pointer addresses the whole document.
type Pointer []string

// ParsePointer parses a JSON Pointer such as "/a~1b/0" into
// Pointer{"a/b", "0"}.
func ParsePointer(s string) (Pointer, error) {
	if s == "" {
		return Pointer{}, nil
	}
	if s[0] != '/' {
		return nil, &Error{Kind: ErrMalformedDirective, Detail: "JSON Pointer " + strconv.Quote(s) + " does not start with /"}
	}
	parts := strings.Split(s[1:], "/")
	p := make(Pointer, len(parts))
	for i, t := range parts {
		u, ok := unescapeToken(t)
		if !ok {
			return nil, &Error{Kind: ErrMalformedDirective, Detail: "JSON Pointer " + strconv.Quote(s) + " has a ~ not followed by 0 or 1"}
		}
		p[i] = u
	}
	return p, nil
}

// String formats p as a JSON Pointer, escaping ~ and / in each token.
func (p Pointer) String() string {
	var b strings.Builder
	for _, t := range p {
		b.WriteByte('/')
		tokenEscaper.WriteString(&b, t)
	}
	return b.String()
}

var tokenEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func unescapeToken(t string) (string, bool) {
	if !strings.Contains(t, "~") {
		return t, true
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] != '~' {
			b.WriteByte(t[i])
			continue
		}
		if i+1 == len(t) {
			return "", false
		}
		i++
		switch t[i] {
		case '0':
			b.WriteByte('~')
		case '1':
			b.WriteByte('/')
		default:
			return "", false
		}
	}
	return b.String(), true
}

// Fragment is the parsed fragment of a reference: an optional anchor name,
// then a JSON Pointer relative to the anchored node, or to the document root
// when there is no anchor.
type Fragment struct {
	// Anchor is the anchor name, or "" for a plain JSON Pointer.
	Anchor string
	// Pointer is the path from the anchored node or the root.
	Pointer Pointer
}

// ParseFragment parses a fragment in its URI form, such as "#iA/sine/rms",
// "iA/sine/rms" or "/a/0". The leading "#" is optional, and percent-encoding
// is decoded before the fragment is read.
func ParseFragment(s string) (Fragment, error) {
	s = strings.TrimPrefix(s, "#")
	dec, err := url.PathUnescape(s)
	if err != nil {
		return Fragment{}, &Error{Kind: ErrMalformedDirective, Detail: "fragment " + strconv.Quote(s) + " is not percent-encoded correctly", Err: err}
	}
	return parseFragment(dec)
}

// parseFragment parses a fragment whose percent-encoding is already decoded.
func parseFragment(f string) (Fragment, error) {
	if f == "" || f[0] == '/' {
		p, err := ParsePointer(f)
		return Fragment{Pointer: p}, err
	}
	name, rest, found := strings.Cut(f, "/")
	if !validAnchor(name) {
		return Fragment{}, &Error{Kind: ErrMalformedDirective, Detail: "fragment " + strconv.Quote(f) + " starts with neither / nor a valid anchor name"}
	}
	fr := Fragment{Anchor: name}
	if found {
		p, err := ParsePointer("/" + rest)
		if err != nil {
			return Fragment{}, err
		}
		fr.Pointer = p
	}
	return fr, nil
}

// String formats f in its URI form, without the leading "#", so that
// ParseFragment(f.String()) returns f.
func (f Fragment) String() string {
	u := url.URL{Fragment: f.Anchor + f.Pointer.String()}
	return u.EscapedFragment()
}

// validAnchor reports whether s matches ^[A-Za-z_][-A-Za-z0-9._]*$.
func validAnchor(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

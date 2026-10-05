package composablejson

import (
	"net/url"
	"slices"
)

// Document is a resolved document.
type Document struct {
	// URI is the document's URI, or nil when it was resolved from bytes with
	// no base URI.
	URI *url.URL
	// Value is the resolved value: map[string]any, []any, json.Number,
	// string, bool or nil. It is the caller's to modify.
	Value any

	anchors map[string]Pointer
}

// Anchors returns the position of each $anchor in Value, by name. A host
// that addresses Value with a reference such as "#iA/sine/rms" finds the
// anchored node here, then follows the rest of the fragment itself:
//
//	frag, _ := composablejson.ParseFragment("#iA/sine/rms")
//	p := append(doc.Anchors()[frag.Anchor], frag.Pointer...)
//
// The map is a new copy on each call, and it describes Value as it was
// resolved, so it does not reflect later changes to Value.
func (d *Document) Anchors() map[string]Pointer {
	out := make(map[string]Pointer, len(d.anchors))
	for name, p := range d.anchors {
		out[name] = slices.Clone(p)
	}
	return out
}

// deepCopy copies v, stopping early, with b exceeded, once it has copied
// more values than b allows.
func deepCopy(v any, b *budget) any {
	if !b.spend() {
		return nil
	}
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if out[k] = deepCopy(x, b); b.exceeded() {
				return nil
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			if out[i] = deepCopy(x, b); b.exceeded() {
				return nil
			}
		}
		return out
	}
	return v
}

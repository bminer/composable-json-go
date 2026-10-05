package composablejson_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	composablejson "github.com/bminer/composable-json-go"
)

// The conformance suite comes from the specification repository, which is a
// submodule at testdata/composable-json. COMPOSABLE_JSON_SUITE points the
// tests at another copy of its tests directory, such as a working tree of the
// specification.
func suiteDir() string {
	if dir := os.Getenv("COMPOSABLE_JSON_SUITE"); dir != "" {
		return dir
	}
	return filepath.Join("testdata", "composable-json", "tests")
}

var errorKinds = map[string]error{
	"invalid-json":            composablejson.ErrInvalidJSON,
	"duplicate-key":           composablejson.ErrDuplicateKey,
	"unresolvable-reference":  composablejson.ErrUnresolvable,
	"missing-base-uri":        composablejson.ErrMissingBaseURI,
	"remote-to-local":         composablejson.ErrRemoteToLocal,
	"cycle":                   composablejson.ErrCycle,
	"duplicate-anchor":        composablejson.ErrDuplicateAnchor,
	"anchor-in-defs":          composablejson.ErrAnchorInDefs,
	"extend-type":             composablejson.ErrExtendType,
	"splice-type":             composablejson.ErrSpliceType,
	"splice-position":         composablejson.ErrSplicePosition,
	"sibling-keys":            composablejson.ErrSiblingKeys,
	"extend-synonym-conflict": composablejson.ErrExtendSynonym,
	"empty-references":        composablejson.ErrEmptyReferences,
	"malformed-directive":     composablejson.ErrMalformedDirective,
	"unknown-directive":       composablejson.ErrUnknownDirective,
}

type suiteFile struct {
	Tests []suiteCase `json:"tests"`
}

type suiteCase struct {
	Description  string                     `json:"description"`
	Documents    map[string]json.RawMessage `json:"documents"`
	RawDocuments map[string]string          `json:"rawDocuments"`
	Root         string                     `json:"root"`
	Options      struct {
		Remote         bool     `json:"remote"`
		HostDirectives []string `json:"hostDirectives"`
		NoBaseURI      bool     `json:"noBaseURI"`
	} `json:"options"`
	Expected json.RawMessage `json:"expected"`
	Error    string          `json:"error"`
}

func TestConformance(t *testing.T) {
	dir := suiteDir()
	files, _ := filepath.Glob(filepath.Join(dir, "cases", "*.json"))
	if len(files) == 0 {
		t.Fatalf("no conformance tests in %s; run `git submodule update --init`", dir)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var suite suiteFile
		if err := json.Unmarshal(data, &suite); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			for _, tc := range suite.Tests {
				t.Run(tc.Description, func(t *testing.T) { runCase(t, tc) })
			}
		})
	}
}

func runCase(t *testing.T, tc suiteCase) {
	base, _ := url.Parse("file:///suite/")
	docs := composablejson.MapLoader{}
	add := func(ref string, data []byte) {
		u, err := base.Parse(ref)
		if err != nil {
			t.Fatalf("document %q: %v", ref, err)
		}
		docs[u.String()] = data
	}
	for ref, data := range tc.Documents {
		add(ref, data)
	}
	for ref, text := range tc.RawDocuments {
		add(ref, []byte(text))
	}
	root := tc.Root
	if root == "" {
		root = "main.json"
	}
	rootURI, _ := base.Parse(root)

	opts := composablejson.Options{
		Local:          map[string]composablejson.Loader{"file": docs},
		HostDirectives: tc.Options.HostDirectives,
	}
	if tc.Options.Remote {
		opts.Remote = map[string]composablejson.Loader{"http": docs, "https": docs}
	}
	var doc *composablejson.Document
	var err error
	if tc.Options.NoBaseURI {
		doc, err = composablejson.ResolveBytes(context.Background(), docs[rootURI.String()], nil, opts)
	} else {
		doc, err = composablejson.Resolve(context.Background(), rootURI, opts)
	}

	if tc.Error != "" {
		kind, ok := errorKinds[tc.Error]
		if !ok {
			t.Fatalf("unknown error code %q", tc.Error)
		}
		if !errors.Is(err, kind) {
			t.Fatalf("want a %s error, got %v", tc.Error, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := json.Marshal(doc.Value)
	if err != nil {
		t.Fatal(err)
	}
	if want, got := canonical(t, tc.Expected), canonical(t, got); want != got {
		t.Fatalf("\nwant %s\n got %s", want, got)
	}
}

// canonical reformats JSON so that equal values compare equal: object keys
// sorted, numbers as written.
func canonical(t *testing.T, data []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

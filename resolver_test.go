package composablejson_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"strings"
	"sync"
	"testing"

	composablejson "github.com/bminer/composable-json-go"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// countingLoader serves a MapLoader and counts loads per URI.
type countingLoader struct {
	docs  composablejson.MapLoader
	mu    sync.Mutex
	loads map[string]int
}

func (l *countingLoader) Load(ctx context.Context, u *url.URL) (io.ReadCloser, error) {
	l.mu.Lock()
	if l.loads == nil {
		l.loads = map[string]int{}
	}
	l.loads[u.String()]++
	l.mu.Unlock()
	return l.docs.Load(ctx, u)
}

func TestResolverCachesAcrossCalls(t *testing.T) {
	loader := &countingLoader{docs: composablejson.MapLoader{
		"file:///t/a.json":    []byte(`{"$extend": "./base.json", "name": "a"}`),
		"file:///t/b.json":    []byte(`{"$extend": "./base.json", "name": "b"}`),
		"file:///t/base.json": []byte(`{"setup": ["boot"], "name": "base"}`),
	}}
	r := composablejson.NewResolver(composablejson.Options{Local: map[string]composablejson.Loader{"file": loader}})
	for _, name := range []string{"a", "b", "a"} {
		doc, err := r.Resolve(context.Background(), mustURL(t, "file:///t/"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"name":"` + name + `","setup":["boot"]}`
		if got := toJSON(t, doc.Value); got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
	for uri, n := range loader.loads {
		if n != 1 {
			t.Errorf("%s loaded %d times, want once", uri, n)
		}
	}

	r.Reset()
	if _, err := r.Resolve(context.Background(), mustURL(t, "file:///t/a.json")); err != nil {
		t.Fatal(err)
	}
	if n := loader.loads["file:///t/base.json"]; n != 2 {
		t.Errorf("after Reset, base.json loaded %d times in all, want 2", n)
	}
}

func TestResolveBytesStandsInForItsBase(t *testing.T) {
	docs := composablejson.MapLoader{
		"file:///t/main.json":   []byte(`{"x": "from disk"}`),
		"file:///t/report.json": []byte(`{"x": {"$ref": "./main.json#/x"}}`),
	}
	r := composablejson.NewResolver(composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}})
	ctx := context.Background()
	resolveReport := func() string {
		doc, err := r.Resolve(ctx, mustURL(t, "file:///t/report.json"))
		if err != nil {
			t.Fatal(err)
		}
		return toJSON(t, doc.Value)
	}

	if got := resolveReport(); got != `{"x":"from disk"}` {
		t.Fatalf("before: got %s", got)
	}
	// main.json in memory references report.json, which references back
	// into main.json: within the call, that must be the in-memory version.
	data := []byte(`{"x": "in memory", "report": {"$ref": "./report.json"}}`)
	doc, err := r.ResolveBytes(ctx, data, mustURL(t, "file:///t/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toJSON(t, doc.Value), `{"report":{"x":"in memory"},"x":"in memory"}`; got != want {
		t.Fatalf("ResolveBytes: got %s, want %s", got, want)
	}
	// Nothing resolved against the in-memory version may be cached.
	if got := resolveReport(); got != `{"x":"from disk"}` {
		t.Fatalf("after: got %s", got)
	}
}

func TestResolveBytesWithoutBase(t *testing.T) {
	doc, err := composablejson.ResolveBytes(context.Background(), []byte(`{"a": 1, "b": {"$ref": "#/a"}}`), nil, composablejson.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.URI != nil {
		t.Errorf("URI = %v, want nil", doc.URI)
	}
	if got := toJSON(t, doc.Value); got != `{"a":1,"b":1}` {
		t.Errorf("got %s", got)
	}
}

func TestValueIsTheCallersToModify(t *testing.T) {
	docs := composablejson.MapLoader{
		"file:///t/main.json": []byte(`{"a": {"$ref": "./base.json"}, "b": {"$ref": "./base.json"}}`),
		"file:///t/base.json": []byte(`{"k": 1}`),
	}
	r := composablejson.NewResolver(composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}})
	doc, err := r.Resolve(context.Background(), mustURL(t, "file:///t/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := doc.Value.(map[string]any)
	root["a"].(map[string]any)["k"] = "changed"
	if got := toJSON(t, root["b"]); got != `{"k":1}` {
		t.Errorf("changing a changed b: %s", got)
	}
	again, err := r.Resolve(context.Background(), mustURL(t, "file:///t/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := toJSON(t, again.Value); got != `{"a":{"k":1},"b":{"k":1}}` {
		t.Errorf("changing a result changed the cache: %s", got)
	}
}

func TestAnchors(t *testing.T) {
	docs := composablejson.MapLoader{
		"file:///t/main.json": []byte(`{
			"inputs": [{"$splice": "./common.json#/inputs"}],
			"limits": {"$anchor": "limits", "cpu": 2}
		}`),
		"file:///t/common.json": []byte(`{"inputs": [{"$anchor": "iA", "sine": {"rms": 0}}]}`),
	}
	doc, err := composablejson.Resolve(context.Background(), mustURL(t, "file:///t/main.json"),
		composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}})
	if err != nil {
		t.Fatal(err)
	}
	anchors := doc.Anchors()
	if got := anchors["iA"].String(); got != "/inputs/0" {
		t.Errorf("iA at %q", got)
	}
	if got := anchors["limits"].String(); got != "/limits" {
		t.Errorf("limits at %q", got)
	}
	anchors["iA"][0] = "changed"
	if got := doc.Anchors()["iA"].String(); got != "/inputs/0" {
		t.Errorf("Anchors returned a shared map: %q", got)
	}
}

func TestCycleErrorNamesTheChain(t *testing.T) {
	docs := composablejson.MapLoader{
		"file:///t/a.json": []byte(`{"p": {"$ref": "./b.json#/q"}}`),
		"file:///t/b.json": []byte(`{"q": {"$ref": "./a.json#/p"}}`),
	}
	_, err := composablejson.Resolve(context.Background(), mustURL(t, "file:///t/a.json"),
		composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}})
	var e *composablejson.Error
	if !errors.As(err, &e) || e.Kind != composablejson.ErrCycle {
		t.Fatalf("want a cycle, got %v", err)
	}
	var chain []string
	for _, l := range e.Chain {
		chain = append(chain, l.String())
	}
	if got, want := strings.Join(chain, " -> "), "file:///t/a.json#/p -> file:///t/b.json#/q -> file:///t/a.json#/p"; got != want {
		t.Errorf("chain %s, want %s", got, want)
	}
}

func TestLoaderErrorIsWrapped(t *testing.T) {
	_, err := composablejson.Resolve(context.Background(), mustURL(t, "file:///t/missing.json"),
		composablejson.Options{Local: map[string]composablejson.Loader{"file": composablejson.MapLoader{}}})
	if !errors.Is(err, composablejson.ErrUnresolvable) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want ErrUnresolvable wrapping fs.ErrNotExist, got %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	docs := composablejson.MapLoader{"file:///t/main.json": []byte(`{}`)}
	_, err := composablejson.Resolve(ctx, mustURL(t, "file:///t/main.json"),
		composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestInvalidOptions(t *testing.T) {
	file := map[string]composablejson.Loader{"file": composablejson.FileLoader{}}
	for name, opts := range map[string]composablejson.Options{
		"spec key as host directive": {HostDirectives: []string{"$ref"}},
		"$id as host directive":      {HostDirectives: []string{"$id"}},
		"host directive without $":   {HostDirectives: []string{"csv"}},
		"scheme local and remote":    {Local: file, Remote: file},
		"nil loader":                 {Local: map[string]composablejson.Loader{"file": nil}},
	} {
		_, err := composablejson.ResolveBytes(context.Background(), []byte(`{}`), nil, opts)
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDocumentURIMustBeAbsolute(t *testing.T) {
	for _, s := range []string{"main.json", "file:///t/main.json#/a"} {
		_, err := composablejson.Resolve(context.Background(), mustURL(t, s), composablejson.Options{})
		if !errors.Is(err, composablejson.ErrUnresolvable) {
			t.Errorf("%s: want ErrUnresolvable, got %v", s, err)
		}
	}
}

func TestConcurrentResolves(t *testing.T) {
	docs := composablejson.MapLoader{
		"file:///t/main.json": []byte(`{"$extend": "./base.json", "a": 2}`),
		"file:///t/base.json": []byte(`{"a": 1, "b": 1}`),
	}
	r := composablejson.NewResolver(composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			doc, err := r.Resolve(context.Background(), mustURL(t, "file:///t/main.json"))
			if err != nil {
				t.Error(err)
				return
			}
			if got := toJSON(t, doc.Value); got != `{"a":2,"b":1}` {
				t.Errorf("got %s", got)
			}
		})
	}
	wg.Wait()
}

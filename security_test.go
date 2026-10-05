package composablejson_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	composablejson "github.com/bminer/composable-json-go"
)

func TestGroups(t *testing.T) {
	docs := composablejson.MapLoader{
		"file:///t/local.json":           []byte(`{"secure": {"$ref": "https://s.example/a.json"}, "plain": {"$ref": "http://p.example/a.json"}}`),
		"https://s.example/a.json":       []byte(`{"k": 1}`),
		"https://s.example/mixed.json":   []byte(`{"x": {"$ref": "http://p.example/a.json"}}`),
		"https://s.example/tolocal.json": []byte(`{"x": {"$ref": "file:///t/local.json"}}`),
		"http://p.example/a.json":        []byte(`{"k": 2}`),
		"http://p.example/upgrade.json":  []byte(`{"x": {"$ref": "https://s.example/a.json"}}`),
		"http://p.example/tolocal.json":  []byte(`{"x": {"$ref": "file:///t/local.json"}}`),
	}
	opts := composablejson.Options{
		Local:    map[string]composablejson.Loader{"file": docs},
		Remote:   map[string]composablejson.Loader{"https": docs},
		Insecure: map[string]composablejson.Loader{"http": docs},
	}
	for uri, want := range map[string]error{
		"file:///t/local.json":           nil,
		"http://p.example/upgrade.json":  nil,
		"https://s.example/mixed.json":   composablejson.ErrInsecureReference,
		"https://s.example/tolocal.json": composablejson.ErrRemoteToLocal,
		"http://p.example/tolocal.json":  composablejson.ErrRemoteToLocal,
	} {
		_, err := composablejson.Resolve(context.Background(), mustURL(t, uri), opts)
		if want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Errorf("%s: want %v, got %v", uri, want, err)
		}
	}
}

// TestRedirects runs an https server and an http server that redirect to
// each other, and checks which redirects the resolver lets HTTPLoader follow.
func TestRedirects(t *testing.T) {
	var secure, plain *httptest.Server
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/to-https/"):
			http.Redirect(w, r, secure.URL+"/"+strings.TrimPrefix(r.URL.Path, "/to-https/"), http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/to-http/"):
			http.Redirect(w, r, plain.URL+"/"+strings.TrimPrefix(r.URL.Path, "/to-http/"), http.StatusFound)
		case r.URL.Path == "/dir/doc.json":
			io.WriteString(w, `{"$extend": "./base.json"}`)
		case r.URL.Path == "/dir/base.json":
			fmt.Fprintf(w, `{"overTLS": %t}`, r.TLS != nil)
		default:
			http.NotFound(w, r)
		}
	}
	secure = httptest.NewTLSServer(http.HandlerFunc(handler))
	defer secure.Close()
	plain = httptest.NewServer(http.HandlerFunc(handler))
	defer plain.Close()

	loader := composablejson.HTTPLoader{Client: secure.Client()}
	opts := composablejson.Options{
		Remote:   map[string]composablejson.Loader{"https": loader},
		Insecure: map[string]composablejson.Loader{"http": loader},
	}
	ctx := context.Background()

	// http -> https is an upgrade, and the document's base URI becomes the
	// https URI, so its relative reference is fetched over https too.
	doc, err := composablejson.Resolve(ctx, mustURL(t, plain.URL+"/to-https/dir/doc.json"), opts)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if got := toJSON(t, doc.Value); got != `{"overTLS":true}` {
		t.Errorf("upgrade: got %s, want base.json from the https server", got)
	}
	if doc.URI.String() != secure.URL+"/dir/doc.json" {
		t.Errorf("upgrade: URI = %s", doc.URI)
	}

	// https -> http is a downgrade, refused before it is followed.
	_, err = composablejson.Resolve(ctx, mustURL(t, secure.URL+"/to-http/dir/doc.json"), opts)
	if !errors.Is(err, composablejson.ErrInsecureReference) {
		t.Errorf("downgrade: want ErrInsecureReference, got %v", err)
	}

	// A redirect to a scheme with no loader is refused too.
	httpsOnly := composablejson.Options{Insecure: map[string]composablejson.Loader{"http": loader}}
	_, err = composablejson.Resolve(ctx, mustURL(t, plain.URL+"/to-https/dir/doc.json"), httpsOnly)
	if !errors.Is(err, composablejson.ErrUnresolvable) {
		t.Errorf("unregistered: want ErrUnresolvable, got %v", err)
	}
}

func TestLimits(t *testing.T) {
	ctx := context.Background()
	resolve := func(docs composablejson.MapLoader, limits composablejson.Limits) error {
		_, err := composablejson.Resolve(ctx, mustURL(t, "file:///t/main.json"), composablejson.Options{
			Local:  map[string]composablejson.Loader{"file": docs},
			Limits: limits,
		})
		return err
	}

	// Each level doubles the output, so 40 levels would be 2^40 values.
	var b strings.Builder
	b.WriteString(`{"l0": [1, 2]`)
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, `, "l%d": [{"$ref": "#/l%d"}, {"$ref": "#/l%d"}]`, i, i-1, i-1)
	}
	b.WriteString(`}`)
	exponential := composablejson.MapLoader{"file:///t/main.json": []byte(b.String())}
	if err := resolve(exponential, composablejson.Limits{}); !errors.Is(err, composablejson.ErrLimit) {
		t.Errorf("exponential output: want ErrLimit, got %v", err)
	}

	// A chain of $ref nodes, each leading to the next.
	b.Reset()
	b.WriteString(`{"n0": 0`)
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, `, "n%d": {"$ref": "#/n%d"}`, i, i-1)
	}
	b.WriteString(`}`)
	chain := composablejson.MapLoader{"file:///t/main.json": []byte(b.String())}
	if err := resolve(chain, composablejson.Limits{MaxDepth: 100}); !errors.Is(err, composablejson.ErrLimit) {
		t.Errorf("long chain: want ErrLimit, got %v", err)
	}
	if err := resolve(chain, composablejson.Limits{}); err != nil {
		t.Errorf("long chain within the default: %v", err)
	}

	many := composablejson.MapLoader{
		"file:///t/main.json": []byte(`{"$extend": ["./a.json", "./b.json"]}`),
		"file:///t/a.json":    []byte(`{}`),
		"file:///t/b.json":    []byte(`{}`),
	}
	if err := resolve(many, composablejson.Limits{MaxDocuments: 2}); !errors.Is(err, composablejson.ErrLimit) {
		t.Errorf("too many documents: want ErrLimit, got %v", err)
	}
	if err := resolve(many, composablejson.Limits{MaxDocuments: 3}); err != nil {
		t.Errorf("documents within the limit: %v", err)
	}

	big := composablejson.MapLoader{"file:///t/main.json": []byte(`{"k": "0123456789"}`)}
	if err := resolve(big, composablejson.Limits{MaxBytes: 10}); !errors.Is(err, composablejson.ErrLimit) {
		t.Errorf("too large: want ErrLimit, got %v", err)
	}
	if err := resolve(big, composablejson.Limits{MaxBytes: 19}); err != nil {
		t.Errorf("exactly at MaxBytes: %v", err)
	}
	if err := resolve(big, composablejson.Limits{MaxBytes: composablejson.NoLimit}); err != nil {
		t.Errorf("NoLimit: %v", err)
	}
}

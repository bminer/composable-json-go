package composablejson_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"

	composablejson "github.com/bminer/composable-json-go"
)

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// Documents are served from memory here; by default a Resolver reads file
// URIs from the local filesystem.
var exampleDocs = composablejson.MapLoader{
	"file:///srv/config/service.base.json": []byte(`{
		"server": {"host": "0.0.0.0", "port": 8080, "tls": false},
		"logging": {"level": "info", "format": "json"}
	}`),
	"file:///srv/config/staging.json": []byte(`{
		"$extend": "./service.base.json",
		"server": {"tls": true},
		"logging": {"level": "debug"}
	}`),
}

func ExampleResolve() {
	opts := composablejson.Options{
		Local: map[string]composablejson.Loader{"file": exampleDocs},
	}
	doc, err := composablejson.Resolve(context.Background(), mustParse("file:///srv/config/staging.json"), opts)
	if err != nil {
		panic(err)
	}
	out, _ := json.Marshal(doc.Value)
	fmt.Println(string(out))
	// Output: {"logging":{"format":"json","level":"debug"},"server":{"host":"0.0.0.0","port":8080,"tls":true}}
}

func ExampleResolver() {
	// One Resolver for many documents: service.base.json is loaded and
	// resolved once, however many documents extend it.
	r := composablejson.NewResolver(composablejson.Options{
		Local: map[string]composablejson.Loader{"file": exampleDocs},
	})
	for _, name := range []string{"staging.json", "service.base.json"} {
		doc, err := r.Resolve(context.Background(), mustParse("file:///srv/config/"+name))
		if err != nil {
			panic(err)
		}
		server := doc.Value.(map[string]any)["server"].(map[string]any)
		fmt.Println(name, "tls:", server["tls"])
	}
	// Output:
	// staging.json tls: true
	// service.base.json tls: false
}

func ExampleResolveBytes() {
	opts := composablejson.Options{
		Local: map[string]composablejson.Loader{"file": exampleDocs},
	}
	data := []byte(`{"$extend": "./service.base.json", "server": {"port": 9090}}`)
	doc, err := composablejson.ResolveBytes(context.Background(), data, mustParse("file:///srv/config/dev.json"), opts)
	if err != nil {
		panic(err)
	}
	fmt.Println(doc.Value.(map[string]any)["server"])
	// Output: map[host:0.0.0.0 port:9090 tls:false]
}

func ExampleDocument_Anchors() {
	// A host format addresses the resolved document with a reference such
	// as "#iA/sine/rms": the node anchored iA, then sine/rms within it.
	data := []byte(`{"inputs": [{"$anchor": "iA", "sine": {"rms": 0, "hz": 60}}]}`)
	doc, err := composablejson.ResolveBytes(context.Background(), data, nil, composablejson.Options{})
	if err != nil {
		panic(err)
	}
	frag, err := composablejson.ParseFragment("#iA/sine/rms")
	if err != nil {
		panic(err)
	}
	p := append(slices.Clone(doc.Anchors()[frag.Anchor]), frag.Pointer...)
	fmt.Println(p)
	// Output: /inputs/0/sine/rms
}

func ExampleOptions_hostDirectives() {
	// A host format declares its own directives. Their values are resolved
	// like any other, and the key is left for the host.
	docs := composablejson.MapLoader{
		"file:///t/trip.json":         []byte(`{"$csv": {"$extend": "./csv.defaults.json", "file": "trip.csv"}}`),
		"file:///t/csv.defaults.json": []byte(`{"delimiter": ",", "header": true}`),
	}
	doc, err := composablejson.Resolve(context.Background(), mustParse("file:///t/trip.json"), composablejson.Options{
		Local:          map[string]composablejson.Loader{"file": docs},
		HostDirectives: []string{"$csv"},
	})
	if err != nil {
		panic(err)
	}
	out, _ := json.Marshal(doc.Value)
	fmt.Println(string(out))
	// Output: {"$csv":{"delimiter":",","file":"trip.csv","header":true}}
}

func ExampleError() {
	data := []byte(`{"$extned": "./service.base.json"}`)
	_, err := composablejson.ResolveBytes(context.Background(), data, mustParse("file:///srv/config/typo.json"), composablejson.Options{})
	var e *composablejson.Error
	if errors.As(err, &e) && errors.Is(err, composablejson.ErrUnknownDirective) {
		fmt.Println(e.Location)
	}
	// Output: file:///srv/config/typo.json#/$extned
}

package composablejson_test

import (
	"context"
	"testing"

	composablejson "github.com/bminer/composable-json-go"
)

// FuzzResolveBytes checks that no input makes resolution panic or hang.
func FuzzResolveBytes(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"$extend": "./other.json", "b": null}`,
		`{"a": [{"$splice": "./other.json#/list"}, {"$ref": "#x/k"}]}`,
		`{"$anchor": "x", "k": {"$ref": "#/k"}}`,
		`{"a": {"$extend": "#/b"}, "b": {"$extend": "#/a"}}`,
		`{"$defs": {"d": {"$anchor": "y"}}, "c": {"$extend": ["#/$defs/d", "./other.json#y"]}}`,
		`[{"$splice": "#"}]`,
	} {
		f.Add([]byte(seed))
	}
	base := mustParse("file:///fuzz/main.json")
	f.Fuzz(func(t *testing.T, data []byte) {
		docs := composablejson.MapLoader{
			"file:///fuzz/main.json":  data,
			"file:///fuzz/other.json": []byte(`{"list": [1, {"$anchor": "y", "k": 2}], "b": {"$ref": "./main.json#/a"}}`),
		}
		opts := composablejson.Options{Local: map[string]composablejson.Loader{"file": docs}}
		_, _ = composablejson.ResolveBytes(context.Background(), data, base, opts)
		_, _ = composablejson.ResolveBytes(context.Background(), data, nil, opts)
	})
}

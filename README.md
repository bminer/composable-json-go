# composable-json-go

[![Go Reference](https://pkg.go.dev/badge/github.com/bminer/composable-json-go.svg)](https://pkg.go.dev/github.com/bminer/composable-json-go)

A Go implementation of
[Composable JSON](https://github.com/bminer/composable-json): reuse, merge and
override values across plain JSON documents.

```json
{
	"$extend": "./service.base.json",
	"server": { "tls": true },
	"logging": { "level": "debug" }
}
```

Composable JSON documents stay ordinary JSON. A few reserved `$` keys (`$ref`,
`$extend`, `$splice`, `$anchor`, `$defs`, `$comment` and `$schema`) say what to
reuse, and resolving a document replaces them with the values they reference.
See the
[specification](https://github.com/bminer/composable-json/blob/master/SPEC.md)
for the full rules.

The **[API reference](https://pkg.go.dev/github.com/bminer/composable-json-go)**
documents every type and function, with runnable examples.

## Install

```bash
go get github.com/bminer/composable-json-go
```

The package is called `composablejson`. It needs Go 1.26 or later and has no
dependencies beyond the standard library.

## Usage

```go
import composablejson "github.com/bminer/composable-json-go"

u, err := composablejson.FileURI("config/staging.json")
if err != nil {
	return err
}
doc, err := composablejson.Resolve(ctx, u, composablejson.Options{})
if err != nil {
	return err
}

// doc.Value holds map[string]any, []any, json.Number, string, bool or nil.
// Numbers keep their exact source text, so round-tripping into a struct is
// lossless:
raw, _ := json.Marshal(doc.Value)
var cfg Config
err = json.Unmarshal(raw, &cfg)
```

A document already in memory resolves with `ResolveBytes`. Its base URI says
where its relative references point; with a nil base, only `#...` and absolute
references work.

```go
doc, err := composablejson.ResolveBytes(ctx, data, base, composablejson.Options{})
```

### Resolving many documents

A `Resolver` caches every document it loads, and the values resolved within it,
across calls. A test runner whose cases all extend the same setup and teardown
documents reads, parses and resolves those once.

```go
r := composablejson.NewResolver(composablejson.Options{})
for _, path := range testFiles {
	u, _ := composablejson.FileURI(path)
	doc, err := r.Resolve(ctx, u)
	// ...
}
r.Reset() // after documents change on disk
```

A `Resolver` is safe for concurrent use, and runs one resolution at a time.
`ResolveBytes` never caches the document it is given, nor anything resolved from
it.

### Loaders

Documents are loaded by scheme. `Options.Local` holds the loaders that read from
this machine, and `Options.Remote` those that fetch over a network. A scheme
with no loader cannot be referenced, and a document loaded remotely may not
reference a local scheme.

| Loader       | Reads                                                                                            |
| ------------ | ------------------------------------------------------------------------------------------------ |
| `FileLoader` | `file:` URIs from the OS filesystem, including Windows drive letters and UNC paths               |
| `FSLoader`   | an `fs.FS`, such as an `embed.FS`, for URIs under a root URI                                     |
| `HTTPLoader` | `http:` and `https:` with a GET request; non-2xx responses are errors, with an optional size cap |
| `MapLoader`  | documents in memory, keyed by absolute URI; useful in tests                                      |
| `LoaderFunc` | anything else                                                                                    |

By default only `file:` is registered. Network access is opt-in:

```go
r := composablejson.NewResolver(composablejson.Options{
	Local:  map[string]composablejson.Loader{"file": composablejson.FileLoader{}},
	Remote: map[string]composablejson.Loader{"https": composablejson.HTTPLoader{}},
})
```

Setting `Local` replaces the default rather than adding to it, which is why
`file` is listed again above.

### Host directives

A format built on Composable JSON can define `$` keys of its own. Declare them,
and they are left in the output for the host to act on, with their values
resolved like any other:

```go
opts := composablejson.Options{HostDirectives: []string{"$csv"}}
```

Any other `$` key is an error, so a misspelled `$extned` fails loudly instead of
silently inheriting nothing.

### Anchors in the result

`Document.Anchors` returns where each `$anchor` ended up in the resolved value,
for hosts that address it with references such as `#iA/sine/rms`:

```go
frag, _ := composablejson.ParseFragment("#iA/sine/rms")
p := append(slices.Clone(doc.Anchors()[frag.Anchor]), frag.Pointer...)
// p is a JSON Pointer into doc.Value, such as /inputs/0/sine/rms
```

### Errors

Every resolution error is a `*composablejson.Error` whose `Kind` is one of the
`Err` variables, one for each error the specification lists. Each names the
offending node's document and JSON Pointer; a cycle lists its full chain, and a
duplicate anchor both places.

```go
_, err := composablejson.Resolve(ctx, u, opts)
switch {
case errors.Is(err, composablejson.ErrCycle):
	var e *composablejson.Error
	errors.As(err, &e)
	fmt.Println(e.Chain) // [file:///srv/a.json#/p file:///srv/b.json#/q file:///srv/a.json#/p]
case errors.Is(err, fs.ErrNotExist):
	// a missing file; the loader's error is wrapped
}
```

## Conformance

The specification's language-neutral conformance suite is a git submodule at
`testdata/composable-json`. Clone with it:

```bash
git clone --recurse-submodules https://github.com/bminer/composable-json-go
```

or fetch it into an existing clone:

```bash
git submodule update --init
```

`go test ./...` then runs every case. To test against another copy of the suite,
such as a working tree of the specification, point `COMPOSABLE_JSON_SUITE` at
its `tests` directory.

## Status

This module implements Composable JSON **1.0.0-draft**, and stays at `v0` until
the specification reaches 1.0.0. Its API may change before then.

## License

[MIT](LICENSE)

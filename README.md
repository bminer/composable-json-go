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

The package is called `composablejson`; the examples below import it as
`compjson`. It needs Go 1.26 or later and has no dependencies beyond the
standard library.

## Usage

```go
import compjson "github.com/bminer/composable-json-go"

u, err := compjson.FileURI("config/staging.json")
if err != nil {
	return err
}
doc, err := compjson.Resolve(ctx, u, compjson.Options{})
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
doc, err := compjson.ResolveBytes(ctx, data, base, compjson.Options{})
```

### Resolving many documents

A `Resolver` caches every document it loads, and the values resolved within it,
across calls. A test runner whose cases all extend the same setup and teardown
documents reads, parses and resolves those once.

```go
r := compjson.NewResolver(compjson.Options{})
for _, path := range testFiles {
	u, _ := compjson.FileURI(path)
	doc, err := r.Resolve(ctx, u)
	// ...
}
r.Reset() // after documents change on disk
```

A `Resolver` is safe for concurrent use, and runs one resolution at a time.
`ResolveBytes` never caches the document it is given, nor anything resolved from
it.

### Loaders

Documents are loaded by scheme, through loaders registered in one of three
groups:

| Group              | For schemes that…                              | Default                  |
| ------------------ | ---------------------------------------------- | ------------------------ |
| `Options.Local`    | read from this machine, such as `file`         | `{"file": FileLoader{}}` |
| `Options.Remote`   | fetch with protection in transit, like `https` | none                     |
| `Options.Insecure` | fetch without it, like `http`                  | none                     |

A scheme with no loader cannot be referenced. A document may reference, or be
redirected to, its own group or a more trusted one, never a less trusted one:

| A document from… | may reach Local | may reach Remote | may reach Insecure |
| ---------------- | :-------------: | :--------------: | :----------------: |
| Local            |       yes       |       yes        |        yes         |
| Remote           |       no        |       yes        |         no         |
| Insecure         |       no        |  yes (upgrade)   |        yes         |

| Loader       | Reads                                                                  |
| ------------ | ---------------------------------------------------------------------- |
| `FileLoader` | `file:` URIs from the OS filesystem, optionally confined to `Roots`    |
| `FSLoader`   | an `fs.FS`, such as an `embed.FS`, for URIs under a root URI           |
| `HTTPLoader` | `http:` and `https:` with a GET request, optionally limited to `Hosts` |
| `MapLoader`  | documents in memory, keyed by absolute URI; useful in tests            |
| `LoaderFunc` | anything else                                                          |

Network access is opt-in:

```go
r := compjson.NewResolver(compjson.Options{
	Remote: map[string]compjson.Loader{"https": compjson.HTTPLoader{}},
})
```

Setting `Local` replaces the default rather than adding to it, so list `file`
there if you still want it.

A loader that follows redirects, as `HTTPLoader` does, checks each one with
`CheckRedirect` before following it and reports where it ended up, which becomes
the document's base URI. An `https` page that redirects to `http` is refused; an
`http` page that redirects to `https` is an upgrade, and works when one
`HTTPLoader` is registered for both.

### Untrusted documents

Resolving a document fetches whatever it references, so a document you did not
write can ask for more than it appears to. When resolving one:

- **Limit the hosts** a remote document can reach, so it cannot read internal
  services:
  `HTTPLoader{Hosts: []string{"cfg.example.com", "*.cdn.example.com"}}`. `Hosts`
  does not stop a permitted name from resolving to an internal address; a
  `Client` whose dialer checks addresses does.
- **Confine local files** to the directories that hold your documents, so `..`
  and links cannot reach anything else:
  `FileLoader{Roots: []string{"/srv/config"}}`.
- **Keep the limits.** `Options.Limits` bounds the documents one call may load
  (1,000), how deeply resolution may nest (10,000), how many values the output
  may hold (1,000,000) and the size of each document (64 MiB). A small document
  can otherwise resolve to an exponentially larger one. Exceeding a limit is an
  `ErrLimit` error; `compjson.NoLimit` removes one.

### Host directives

A format built on Composable JSON can define `$` keys of its own. Declare them,
and they are left in the output for the host to act on, with their values
resolved like any other:

```go
opts := compjson.Options{HostDirectives: []string{"$csv"}}
```

Any other `$` key is an error, so a misspelled `$extned` fails loudly instead of
silently inheriting nothing.

### Anchors in the result

`Document.Anchors` returns where each `$anchor` ended up in the resolved value,
for hosts that address it with references such as `#iA/sine/rms`:

```go
frag, _ := compjson.ParseFragment("#iA/sine/rms")
p := append(slices.Clone(doc.Anchors()[frag.Anchor]), frag.Pointer...)
// p is a JSON Pointer into doc.Value, such as /inputs/0/sine/rms
```

### Errors

Every resolution error is a `*compjson.Error` whose `Kind` is one of the `Err`
variables, one for each error the specification lists. Each names the offending
node's document and JSON Pointer; a cycle lists its full chain, and a duplicate
anchor both places.

```go
_, err := compjson.Resolve(ctx, u, opts)
switch {
case errors.Is(err, compjson.ErrCycle):
	var e *compjson.Error
	errors.As(err, &e)
	fmt.Println(e.Chain) // [file:///srv/a.json#/p file:///srv/b.json#/q file:///srv/a.json#/p]
case errors.Is(err, fs.ErrNotExist):
	// a missing file; the loader's error is wrapped
}
```

## Conformance

The specification's language-neutral conformance suite comes from its own
repository, a git submodule at `composable-json`, so the cases are in
`composable-json/tests`. Clone with it:

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

// Package composablejson resolves Composable JSON documents.
//
// Composable JSON lets plain JSON documents reuse and override values from
// other documents through a few reserved $-prefixed keys: $ref, $extend,
// $splice, $anchor, $defs, $comment and $schema. A resolver replaces each
// directive with the value it references and produces ordinary JSON. The
// specification is at https://github.com/bminer/composable-json.
//
// The simplest use resolves a single document:
//
//	u, _ := composablejson.FileURI("config/staging.json")
//	doc, err := composablejson.Resolve(ctx, u, composablejson.Options{})
//
// A [Resolver] keeps a cache across calls, so documents shared by many others,
// such as a common base configuration, are loaded and resolved once.
//
// Resolved values have the shapes encoding/json produces with UseNumber:
// map[string]any, []any, json.Number, string, bool and nil. Numbers keep
// their exact source text.
//
// Every error found in a document is an [*Error] whose Kind is one of the Err
// variables, such as [ErrCycle], so errors.Is identifies it. Invalid
// [Options] are reported as plain errors, and a cancelled context as the
// context's error.
package composablejson

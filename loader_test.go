package composablejson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFilePath(t *testing.T) {
	for _, tc := range []struct {
		goos, uri, want string
	}{
		{"linux", "file:///srv/app/a.json", "/srv/app/a.json"},
		{"linux", "file://localhost/srv/a.json", "/srv/a.json"},
		{"linux", "file:///srv/a%20b.json", "/srv/a b.json"},
		{"windows", "file:///C:/app/a.json", `C:\app\a.json`},
		{"windows", "file://server/share/a.json", `\\server\share\a.json`},
		{"windows", "file:///c:/a%23b.json", `c:\a#b.json`},
	} {
		u, _ := url.Parse(tc.uri)
		got, err := filePath(tc.goos, u)
		if err != nil || got != tc.want {
			t.Errorf("filePath(%s, %s) = %q, %v; want %q", tc.goos, tc.uri, got, err, tc.want)
		}
	}
	for _, uri := range []string{"https://x/a.json", "file://server/a.json"} {
		u, _ := url.Parse(uri)
		if _, err := filePath("linux", u); err == nil {
			t.Errorf("filePath(linux, %s): no error", uri)
		}
	}
}

func TestFileURI(t *testing.T) {
	for _, tc := range []struct {
		goos, path, want string
	}{
		{"linux", "/srv/app/a b.json", "file:///srv/app/a%20b.json"},
		{"windows", `C:\app\a.json`, "file:///C:/app/a.json"},
		{"windows", `\\server\share\a.json`, "file://server/share/a.json"},
		{"windows", `C:\a#b?.json`, "file:///C:/a%23b%3F.json"},
	} {
		got := fileURI(tc.goos, tc.path)
		if got.String() != tc.want {
			t.Errorf("fileURI(%s, %q) = %s, want %s", tc.goos, tc.path, got, tc.want)
		}
		back, err := filePath(tc.goos, got)
		if err != nil || back != tc.path {
			t.Errorf("filePath(fileURI(%q)) = %q, %v", tc.path, back, err)
		}
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustFileURI(t *testing.T, path string) *url.URL {
	t.Helper()
	u, err := FileURI(path)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func readAll(t *testing.T, res *Resource) string {
	t.Helper()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFileLoader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a #1.json")
	writeFile(t, path, `{"k": 1}`)
	doc, err := Resolve(context.Background(), mustFileURI(t, path), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := doc.Value.(map[string]any); m["k"] == nil {
		t.Errorf("got %v", doc.Value)
	}
}

func TestFileLoaderRoots(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	writeFile(t, filepath.Join(root, "cfg", "a.json"), `{}`)
	writeFile(t, filepath.Join(dir, "secret.json"), `{}`)
	l := FileLoader{Roots: []string{root}}
	ctx := context.Background()

	res, err := l.Load(ctx, mustFileURI(t, filepath.Join(root, "cfg", "a.json")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	outside := mustFileURI(t, filepath.Join(dir, "secret.json"))
	if _, err := l.Load(ctx, outside); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("outside the roots: want ErrOutsideRoots, got %v", err)
	}
	dotdot, _ := url.Parse(mustFileURI(t, root).String() + "/cfg/../../secret.json")
	if _, err := l.Load(ctx, dotdot); err == nil {
		t.Error("through ..: no error")
	}

	// A linked directory inside the root that leads outside it.
	linkDir(t, dir, filepath.Join(root, "escape"))
	if _, err := l.Load(ctx, mustFileURI(t, filepath.Join(root, "escape", "secret.json"))); err == nil {
		t.Error("through a link leading outside: no error")
	}
}

// linkDir makes link a symbolic link to the directory target, or on Windows
// without the privilege that needs, a junction.
func linkDir(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err != nil && runtime.GOOS == "windows" {
		out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if jerr == nil {
			return
		}
		err = fmt.Errorf("%v; mklink /J: %v: %s", err, jerr, out)
	}
	if err != nil {
		t.Skipf("cannot link a directory here: %v", err)
	}
}

// TestFileLoaderLinkBase checks that a document reached through a link
// resolves its relative references from the link's location, not from the
// file the link points to.
func TestFileLoaderLinkBase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "shared", "app.json"), `{"$extend": "../base.json"}`)
	writeFile(t, filepath.Join(dir, "base.json"), `{"from": "beside the target"}`)
	writeFile(t, filepath.Join(dir, "cfg", "base.json"), `{"from": "beside the link"}`)
	linkDir(t, filepath.Join(dir, "shared"), filepath.Join(dir, "cfg", "linked"))
	doc, err := Resolve(context.Background(), mustFileURI(t, filepath.Join(dir, "cfg", "linked", "app.json")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Value.(map[string]any)["from"]; got != "beside the link" {
		t.Errorf("base.json from %v, want beside the link", got)
	}
}

func TestFSLoader(t *testing.T) {
	root, _ := url.Parse("file:///srv/app/")
	l := FSLoader{FS: fstest.MapFS{"cfg/a.json": {Data: []byte(`{}`)}}, Root: root}
	for uri, ok := range map[string]bool{
		"file:///srv/app/cfg/a.json":        true,
		"file:///srv/app/cfg/missing.json":  false,
		"file:///srv/other/cfg/a.json":      false,
		"file:///srv/app/../app/cfg/a.json": false,
		"https://x/srv/app/cfg/a.json":      false,
	} {
		u, _ := url.Parse(uri)
		res, err := l.Load(context.Background(), u)
		if ok != (err == nil) {
			t.Errorf("Load(%s): %v", uri, err)
		}
		if err == nil {
			res.Body.Close()
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Load(%s): want fs.ErrNotExist, got %v", uri, err)
		}
	}
}

func TestHTTPLoader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.json":
			io.WriteString(w, `{"k": 1}`)
		case "/moved.json":
			http.Redirect(w, r, "/a.json", http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	l := HTTPLoader{Client: srv.Client()}
	parse := func(path string) *url.URL {
		u, _ := url.Parse(srv.URL + path)
		return u
	}

	res, err := l.Load(ctx, parse("/a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URI != nil {
		t.Errorf("no redirect, but URI = %s", res.URI)
	}
	if got := readAll(t, res); got != `{"k": 1}` {
		t.Errorf("got %q", got)
	}

	res, err = l.Load(ctx, parse("/moved.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URI == nil || res.URI.String() != srv.URL+"/a.json" {
		t.Errorf("after a redirect, URI = %v", res.URI)
	}
	readAll(t, res)

	if _, err := l.Load(ctx, parse("/missing.json")); err == nil {
		t.Error("404: no error")
	}

	host := parse("/").Hostname()
	if _, err := (HTTPLoader{Client: srv.Client(), Hosts: []string{"other.example"}}).Load(ctx, parse("/a.json")); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("host not in Hosts: want ErrHostNotAllowed, got %v", err)
	}
	res, err = HTTPLoader{Client: srv.Client(), Hosts: []string{"other.example", strings.ToUpper(host)}}.Load(ctx, parse("/a.json"))
	if err != nil {
		t.Errorf("host in Hosts: %v", err)
	} else {
		readAll(t, res)
	}
}

func TestHTTPLoaderHostsWildcard(t *testing.T) {
	l := HTTPLoader{Hosts: []string{"*.example.com", "exact.org"}}
	for host, want := range map[string]bool{
		"cfg.example.com":       true,
		"a.b.example.com:8443":  true,
		"example.com":           false,
		"badexample.com":        false,
		"exact.org":             true,
		"sub.exact.org":         false,
		"cfg.example.com.evil.": false,
	} {
		u := &url.URL{Scheme: "https", Host: host}
		if got := l.allowed(u); got != want {
			t.Errorf("allowed(%s) = %v, want %v", host, got, want)
		}
	}
}

package composablejson

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

func TestFileLoader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a #1.json")
	if err := os.WriteFile(path, []byte(`{"k": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := FileURI(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Resolve(context.Background(), u, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := doc.Value.(map[string]any); m["k"] == nil {
		t.Errorf("got %v", doc.Value)
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
		rc, err := l.Load(context.Background(), u)
		if ok != (err == nil) {
			t.Errorf("Load(%s): %v", uri, err)
		}
		if err == nil {
			rc.Close()
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
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	l := HTTPLoader{Client: srv.Client()}
	u, _ := url.Parse(srv.URL + "/a.json")
	rc, err := l.Load(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != `{"k": 1}` {
		t.Errorf("got %q", data)
	}

	missing, _ := url.Parse(srv.URL + "/missing.json")
	if _, err := l.Load(ctx, missing); err == nil {
		t.Error("404: no error")
	}

	small := HTTPLoader{Client: srv.Client(), MaxBytes: 4}
	rc, err = small.Load(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err == nil {
		t.Error("MaxBytes: no error")
	}
	rc.Close()
}

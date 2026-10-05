package composablejson

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A Loader retrieves documents. Load is called with an absolute URI that has
// no fragment, and returns the document's content; the resolver closes it.
// A Loader does not parse documents or follow references, and it is only
// called for the scheme it is registered under in [Options].
type Loader interface {
	Load(ctx context.Context, uri *url.URL) (io.ReadCloser, error)
}

// LoaderFunc adapts a function to a [Loader].
type LoaderFunc func(ctx context.Context, uri *url.URL) (io.ReadCloser, error)

// Load calls f.
func (f LoaderFunc) Load(ctx context.Context, uri *url.URL) (io.ReadCloser, error) {
	return f(ctx, uri)
}

// FileLoader reads file URIs from the local filesystem, converting each to a
// native path with [FilePath].
type FileLoader struct{}

// Load opens the file uri names.
func (FileLoader) Load(ctx context.Context, uri *url.URL) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := FilePath(uri)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// FSLoader reads documents from an [fs.FS], such as an embed.FS or the result
// of os.DirFS. A URI under Root names the file at the rest of its path: with
// Root file:///srv/app/, file:///srv/app/cfg/a.json is "cfg/a.json". Any
// other URI does not exist.
type FSLoader struct {
	FS   fs.FS
	Root *url.URL
}

// Load opens the file uri names within l.FS.
func (l FSLoader) Load(ctx context.Context, uri *url.URL) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, ok := l.name(uri)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: uri.String(), Err: fs.ErrNotExist}
	}
	return l.FS.Open(name)
}

func (l FSLoader) name(uri *url.URL) (string, bool) {
	if l.Root == nil || !strings.EqualFold(uri.Scheme, l.Root.Scheme) || uri.Host != l.Root.Host || uri.Opaque != "" {
		return "", false
	}
	root := l.Root.Path
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	rest, ok := strings.CutPrefix(uri.Path, root)
	if !ok || !fs.ValidPath(rest) {
		return "", false
	}
	return rest, true
}

// HTTPLoader fetches http and https URIs with a GET request. A response
// whose status is not 2xx is an error, and so is a body longer than MaxBytes,
// when MaxBytes is positive.
//
// Relative references in a fetched document resolve against the URI that
// was requested, even when the server redirects.
type HTTPLoader struct {
	// Client sends the requests. Nil means http.DefaultClient.
	Client *http.Client
	// MaxBytes limits the size of a document. Zero means no limit.
	MaxBytes int64
}

// Load fetches uri.
func (l HTTPLoader) Load(ctx context.Context, uri *url.URL) (io.ReadCloser, error) {
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/composable+json, application/json;q=0.9, */*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", uri, resp.Status)
	}
	if l.MaxBytes > 0 {
		return &limitedBody{ReadCloser: resp.Body, left: l.MaxBytes, max: l.MaxBytes}, nil
	}
	return resp.Body, nil
}

// limitedBody fails, rather than stopping silently, once more than max bytes
// have been read.
type limitedBody struct {
	io.ReadCloser
	left, max int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	if b.left -= int64(n); b.left < 0 {
		return 0, fmt.Errorf("document exceeds %d bytes", b.max)
	}
	return n, err
}

// MapLoader serves documents from memory, keyed by absolute URI in the form
// url.URL.String produces, such as "file:///t/main.json". It suits tests and
// generated documents, and can serve any scheme.
type MapLoader map[string][]byte

// Load returns the document stored under uri.
func (m MapLoader) Load(_ context.Context, uri *url.URL) (io.ReadCloser, error) {
	data, ok := m[uri.String()]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: uri.String(), Err: fs.ErrNotExist}
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// FilePath converts a file URI to a native path. On Windows,
// file:///C:/app/a.json becomes C:\app\a.json, and file://server/share/a.json
// becomes \\server\share\a.json.
func FilePath(u *url.URL) (string, error) { return filePath(runtime.GOOS, u) }

func filePath(goos string, u *url.URL) (string, error) {
	if !strings.EqualFold(u.Scheme, "file") || u.Opaque != "" {
		return "", fmt.Errorf("%s is not a file URI", u)
	}
	host := u.Host
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	p := u.Path
	if goos != "windows" {
		if host != "" {
			return "", fmt.Errorf("%s names a remote host", u)
		}
		return p, nil
	}
	if host != "" {
		return `\\` + host + strings.ReplaceAll(p, "/", `\`), nil
	}
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' && isLetter(p[1]) {
		p = p[1:]
	}
	return strings.ReplaceAll(p, "/", `\`), nil
}

// FileURI converts a native path to an absolute file URI, making it absolute
// first if it is relative.
func FileURI(path string) (*url.URL, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return fileURI(runtime.GOOS, abs), nil
}

func fileURI(goos, p string) *url.URL {
	if goos == "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
		if rest, ok := strings.CutPrefix(p, "//"); ok {
			host, path, _ := strings.Cut(rest, "/")
			return &url.URL{Scheme: "file", Host: host, Path: "/" + path}
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
	}
	return &url.URL{Scheme: "file", Path: p}
}

func isLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

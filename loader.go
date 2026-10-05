package composablejson

import (
	"bytes"
	"context"
	"errors"
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
// no fragment, and only for a scheme the Loader is registered under in
// [Options]. A Loader does not parse documents or follow references.
//
// A Loader that follows redirects, or anything like them, should call
// [CheckRedirect] before each one, and report where it ended up in
// [Resource.URI].
type Loader interface {
	Load(ctx context.Context, uri *url.URL) (*Resource, error)
}

// Resource is a loaded document.
type Resource struct {
	// Body is the document's content. The resolver closes it.
	Body io.ReadCloser
	// URI is where the content came from, if not the URI requested: after
	// a redirect, say. It becomes the document's base URI. Nil means the
	// URI requested.
	URI *url.URL
}

// LoaderFunc adapts a function to a [Loader].
type LoaderFunc func(ctx context.Context, uri *url.URL) (*Resource, error)

// Load calls f.
func (f LoaderFunc) Load(ctx context.Context, uri *url.URL) (*Resource, error) {
	return f(ctx, uri)
}

// FileLoader reads file URIs from the local filesystem, converting each to a
// native path with [FilePath].
type FileLoader struct {
	// Roots lists the directories files may be read from. A file outside
	// them cannot be read, and neither can one reached through "..", or
	// through a symbolic link or junction that leads outside them. Nil
	// means any file.
	Roots []string
}

// ErrOutsideRoots is returned by a [FileLoader] for a file outside its
// Roots.
var ErrOutsideRoots = errors.New("file is outside the loader's roots")

// Load opens the file uri names.
func (l FileLoader) Load(ctx context.Context, uri *url.URL) (*Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := FilePath(uri)
	if err != nil {
		return nil, err
	}
	if l.Roots == nil {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		return &Resource{Body: f}, nil
	}
	for _, root := range l.Roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		// os.Root refuses any path, including one through a link, that
		// leads outside the root.
		r, err := os.OpenRoot(abs)
		if err != nil {
			return nil, err
		}
		f, err := r.Open(rel)
		r.Close()
		if err != nil {
			return nil, err
		}
		return &Resource{Body: f}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: p, Err: ErrOutsideRoots}
}

// FSLoader reads documents from an [fs.FS], such as an embed.FS. A URI under
// Root names the file at the rest of its path: with Root file:///srv/app/,
// file:///srv/app/cfg/a.json is "cfg/a.json". Any other URI does not exist.
type FSLoader struct {
	FS   fs.FS
	Root *url.URL
}

// Load opens the file uri names within l.FS.
func (l FSLoader) Load(ctx context.Context, uri *url.URL) (*Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, ok := l.name(uri)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: uri.String(), Err: fs.ErrNotExist}
	}
	f, err := l.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &Resource{Body: f}, nil
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
// whose status is not 2xx is an error.
//
// It follows redirects, checking each with [CheckRedirect] and against
// Hosts before it is followed, so a redirect never downgrades https to http,
// and it reports the URI it ended up at, which becomes the document's base
// URI.
type HTTPLoader struct {
	// Client sends the requests. Nil means http.DefaultClient. A
	// CheckRedirect it sets runs after HTTPLoader's own checks.
	Client *http.Client
	// Hosts lists the hosts that may be fetched, checked on the first
	// request and on every redirect. An entry "*.example.com" matches any
	// subdomain of example.com. Ports are ignored. Nil means any host.
	//
	// Hosts does not stop a permitted name from resolving to an internal
	// address; a Client whose dialer checks addresses can.
	Hosts []string
}

// ErrHostNotAllowed is returned by an [HTTPLoader] for a host not in its
// Hosts.
var ErrHostNotAllowed = errors.New("host is not in the loader's Hosts")

// Load fetches uri.
func (l HTTPLoader) Load(ctx context.Context, uri *url.URL) (*Resource, error) {
	if !l.allowed(uri) {
		return nil, fmt.Errorf("GET %s: %w", uri, ErrHostNotAllowed)
	}
	base := l.Client
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	next := base.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := CheckRedirect(req.Context(), via[len(via)-1].URL, req.URL); err != nil {
			return err
		}
		if !l.allowed(req.URL) {
			return ErrHostNotAllowed
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
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
	res := &Resource{Body: resp.Body}
	if final := resp.Request.URL; final.String() != uri.String() {
		u := *final
		u.Fragment, u.RawFragment = "", ""
		res.URI = &u
	}
	return res, nil
}

func (l HTTPLoader) allowed(u *url.URL) bool {
	if l.Hosts == nil {
		return true
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range l.Hosts {
		h = strings.ToLower(h)
		if parent, ok := strings.CutPrefix(h, "*."); ok {
			if strings.HasSuffix(host, "."+parent) {
				return true
			}
		} else if host == h {
			return true
		}
	}
	return false
}

// MapLoader serves documents from memory, keyed by absolute URI in the form
// url.URL.String produces, such as "file:///t/main.json". It suits tests and
// generated documents, and can serve any scheme.
type MapLoader map[string][]byte

// Load returns the document stored under uri.
func (m MapLoader) Load(_ context.Context, uri *url.URL) (*Resource, error) {
	data, ok := m[uri.String()]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: uri.String(), Err: fs.ErrNotExist}
	}
	return &Resource{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

// limitedBody fails, rather than stopping silently, once more than max bytes
// have been read.
type limitedBody struct {
	r         io.Reader
	key       string
	left, max int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.left {
		// Read one byte past the limit, to tell a document of exactly max
		// bytes from a longer one.
		p = p[:b.left+1]
	}
	n, err := b.r.Read(p)
	if b.left -= int64(n); b.left < 0 {
		return 0, &Error{Kind: ErrLimit, Location: Location{URI: b.key}, Detail: fmt.Sprintf("the document exceeds %d bytes", b.max)}
	}
	return n, err
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

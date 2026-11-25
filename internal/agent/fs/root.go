package fs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const Private = ".dockroom"

var ErrUnsafe = errors.New("unsafe or unsupported filesystem object")
var ErrRootChanged = errors.New("registered root identity changed or unavailable")

type Root struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
}

type identity struct{ volume, object string }

func (id identity) String() string { return id.volume + ":" + id.object }

// ObjectIdentity identifies an already opened object without looking up a path.
func ObjectIdentity(f *os.File) (string, error) {
	id, _, err := identify(f)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func Part(s string) bool {
	if s == "" || s == "." || s == ".." || strings.EqualFold(s, Private) || !utf8.ValidString(s) || strings.ContainsAny(s, "\\/:\x00") {
		return false
	}
	return platformPart(s)
}

func rootParts(path string) (string, []string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, ErrUnsafe
	}
	base := filepath.VolumeName(path) + string(filepath.Separator)
	tail := strings.TrimPrefix(path, base)
	if tail == "" {
		return "", nil, ErrUnsafe
	}
	parts := strings.Split(tail, string(filepath.Separator))
	for _, p := range parts {
		if !Part(p) {
			return "", nil, ErrUnsafe
		}
	}
	return base, parts, nil
}

func (r Root) root() (*os.File, []identity, error) {
	base, parts, err := rootParts(r.Path)
	if err != nil {
		return nil, nil, err
	}
	f, err := start(base)
	if err != nil {
		return nil, nil, err
	}
	chain := make([]identity, 0, len(parts)+1)
	for i := 0; ; i++ {
		id, _, err := identify(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		chain = append(chain, id)
		if i == len(parts) {
			if r.Identity != "" && id.String() != r.Identity {
				f.Close()
				return nil, nil, ErrRootChanged
			}
			return f, chain, nil
		}
		child, err := open(f, parts[i], true, false, true)
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		f = child
	}
}

func Pin(path string) (string, error) {
	f, chain, err := (Root{Path: path}).root()
	if err != nil {
		return "", err
	}
	defer f.Close()
	return chain[len(chain)-1].String(), nil
}

// Validate rejects overlapping roots by physical identity, not text prefix.
func Validate(roots []Root) error {
	chains := make([][]identity, 0, len(roots))
	for _, r := range roots {
		if r.Identity == "" {
			return ErrRootChanged
		}
		f, chain, err := r.root()
		if err != nil {
			return err
		}
		f.Close()
		chains = append(chains, chain)
	}
	for i, a := range chains {
		for j, b := range chains {
			if i == j {
				continue
			}
			for _, ancestor := range b {
				if a[len(a)-1] == ancestor {
					return fmt.Errorf("overlapping roots: %w", ErrUnsafe)
				}
			}
		}
	}
	return nil
}

func (r Root) OpenDir(parts []string) (*os.File, error) {
	if r.Identity == "" {
		return nil, ErrRootChanged
	}
	if len(parts) > 128 {
		return nil, ErrUnsafe
	}
	for _, p := range parts {
		if !Part(p) {
			return nil, ErrUnsafe
		}
	}
	f, _, err := r.root()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRootChanged, err)
	}
	for _, p := range parts {
		child, err := open(f, p, true, false, false)
		f.Close()
		if err != nil {
			return nil, err
		}
		f = child
	}
	return f, nil
}

// PartsFromPath only translates text. A subsequent handle open is mandatory.
func (r Root) PartsFromPath(native string) ([]string, error) {
	if !filepath.IsAbs(native) || filepath.Clean(native) != native {
		return nil, ErrUnsafe
	}
	rel, err := filepath.Rel(r.Path, native)
	if err != nil {
		return nil, ErrUnsafe
	}
	if rel == "." {
		return []string{}, nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts {
		if !Part(p) {
			return nil, ErrUnsafe
		}
	}
	return parts, nil
}

func (r Root) Native(parts []string) string {
	return filepath.Join(append([]string{r.Path}, parts...)...)
}

// Metadata inspects the entry through the already opened parent; no path-based stat.
func Metadata(parent *os.File, name string) (os.FileInfo, string, error) {
	if !Part(name) {
		return nil, "", ErrUnsafe
	}
	f, err := open(parent, name, false, false, false)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	return FileVersion(f)
}

func FileVersion(f *os.File) (os.FileInfo, string, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	id, change, err := identify(f)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s:%s:%d:%d", id.String(), change, info.Size(), info.ModTime().UnixNano()))
	return info, fmt.Sprintf("%x", sum), nil
}

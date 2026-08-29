// Package artwork stores downloaded images on the filesystem and hands them
// back for serving.
//
// The bytes live under the cache directory rather than in Postgres, and the
// record of which image an item has lives in Postgres rather than on disk. See
// migration 0012 for why the split falls there.
//
// This package owns exactly one hard problem: a download that dies halfway
// must never leave a partial file where a reader will find it. Everything
// below follows from that.
package artwork

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"uuid"
)

// subdir is the cache subtree this package owns. Everything it writes and
// everything it sweeps is under here, so it can never touch another
// subsystem's cache.
const subdir = "images"

// tmpPrefix marks a download in progress.
//
// A distinct prefix rather than a plain suffix, so a sweep can identify a
// leftover with a prefix match and can never mistake a stored image for one.
const tmpPrefix = ".tmp-"

// DefaultMaxBytes caps a single image.
//
// A poster is a few hundred kilobytes and a 1280-wide backdrop under two
// megabytes, so this is roughly an order of magnitude of headroom. It is not
// tuning: it is the bound that stops a broken or hostile response filling the
// cache directory, which is a new failure mode for this slice because it is
// the first one where a failed fetch leaves bytes on disk rather than an
// absent row.
const DefaultMaxBytes = 32 << 20

// ErrTooLarge is returned when a response exceeds the size cap. The partial
// file is removed before it is returned.
var ErrTooLarge = errors.New("image exceeds the size limit")

// contentTypeExt maps the image types Reelix accepts onto a file extension.
//
// An allow-list, and it does double duty: it is also the check that a
// downloaded response is an image at all. Without it an HTML error page from a
// CDN gets stored as a poster and served to clients as image/jpeg — a failure
// that looks like a corrupt image rather than like a failed download, which is
// the expensive kind to diagnose.
var contentTypeExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// ExtensionFor returns the file extension for a content type, reporting
// whether it is an image type Reelix stores.
func ExtensionFor(contentType string) (string, bool) {
	ext, ok := contentTypeExt[normalizeContentType(contentType)]
	return ext, ok
}

// normalizeContentType strips parameters and case from a Content-Type header,
// so that "image/jpeg; charset=binary" and "IMAGE/JPEG" both match.
func normalizeContentType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

// Store reads and writes images under a cache directory.
type Store struct {
	root string

	// MaxBytes caps one image. Zero means DefaultMaxBytes.
	MaxBytes int64
}

// NewStore returns a store rooted at cacheDir.
func NewStore(cacheDir string) *Store {
	return &Store{root: filepath.Join(cacheDir, subdir)}
}

// Saved describes an image that is durably on disk.
type Saved struct {
	// Path is relative to the cache directory, so remounting or moving the
	// cache does not invalidate every stored row.
	Path string

	// Tag is 32 lowercase hex characters: the first half of the SHA-256 of the
	// file content.
	//
	// The value is ours to choose. Every recorded reference tag is an opaque
	// 32-hex digest and nothing about its derivation is observable, so this
	// satisfies the whole observed contract — 32 lowercase hex, stable while
	// the image is unchanged, different when it changes — and is computed from
	// the download stream at no extra cost. Deriving it from the CONTENT is
	// what makes cache-busting correct by construction rather than by
	// remembering to bump something.
	Tag string

	ContentType string
	Bytes       int64
}

// Save writes an image atomically and reports where it went.
//
// THE ORDERING HERE IS THE POINT, and it continues at the caller: bytes are
// written to a temporary name, fsynced, renamed onto the final name, and only
// then does the caller write the database row.
//
// Rename within a directory is atomic, so a reader either sees the whole image
// or no image at all — never the leading 40KB of a JPEG, which decodes to a
// half-drawn poster rather than to an error.
//
// A crash between the rename and the row leaves an orphan file and no row: the
// next pass re-downloads and renames over it, and nothing ever advertised it.
// The reverse order would advertise a tag for a file that does not exist, which
// is the "a client asks for an image the server said it had" failure this whole
// slice was told to avoid.
func (s *Store) Save(
	itemID uuid.UUID, imageType, contentType string, r io.Reader,
) (Saved, error) {
	ext, ok := ExtensionFor(contentType)
	if !ok {
		return Saved{}, fmt.Errorf("refusing to store content type %q as an image", contentType)
	}

	dir := s.dir(itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Saved{}, fmt.Errorf("creating the image directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, tmpPrefix+imageType+"-*")
	if err != nil {
		return Saved{}, fmt.Errorf("creating a temporary image file: %w", err)
	}
	tmpName := tmp.Name()

	// Every failure past this point removes the temporary file. A leftover is
	// swept eventually, but leaving one behind on an ordinary error would mean
	// the sweep is load-bearing rather than a backstop.
	written, err := s.writeTemp(tmp, r)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return Saved{}, err
	}

	final := filepath.Join(dir, imageType+ext)
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return Saved{}, fmt.Errorf("moving the image into place: %w", err)
	}

	// Fsync the DIRECTORY, not just the file. The file's own fsync makes its
	// contents durable; only this makes the rename durable. Without it a host
	// power loss can leave the row (which Postgres fsynced) pointing at a name
	// that no longer exists — the exact state the ordering above exists to
	// prevent, arriving by another route.
	//
	// It is not fatal if it fails: a missing file is recovered by the next
	// refresh, because the pass stats what it thinks it has.
	syncDir(dir)

	return Saved{
		Path:        s.relative(final),
		Tag:         written.tag,
		ContentType: normalizeContentType(contentType),
		Bytes:       written.size,
	}, nil
}

type writeResult struct {
	tag  string
	size int64
}

// writeTemp copies r into f, hashing as it goes, and makes the bytes durable.
func (s *Store) writeTemp(f *os.File, r io.Reader) (writeResult, error) {
	max := s.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}

	sum := sha256.New()

	// Read one byte past the cap so that hitting it exactly is not mistaken
	// for exceeding it.
	size, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(r, max+1))
	if err != nil {
		return writeResult{}, fmt.Errorf("downloading the image: %w", err)
	}
	if size > max {
		return writeResult{}, ErrTooLarge
	}
	if size == 0 {
		return writeResult{}, errors.New("the image response was empty")
	}

	if err := f.Sync(); err != nil {
		return writeResult{}, fmt.Errorf("flushing the image to disk: %w", err)
	}
	if err := f.Close(); err != nil {
		return writeResult{}, fmt.Errorf("closing the image: %w", err)
	}

	// The first 32 hex characters, which is the width the observed tags have.
	// 128 bits is far beyond what distinguishing a library's worth of posters
	// requires.
	return writeResult{tag: hex.EncodeToString(sum.Sum(nil))[:32], size: size}, nil
}

// Open opens a stored image for serving.
//
// relPath comes from the database and is joined under the store's root, never
// used as given: a row is not a trusted path source, and a stored '..' must not
// be able to read outside the cache.
func (s *Store) Open(relPath string) (*os.File, os.FileInfo, error) {
	full, err := s.resolve(relPath)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// Exists reports whether a stored image is still on disk.
//
// This is what makes putting the bytes in /cache honest rather than merely
// arguable. The refresh pass calls it for every row it thinks it has, so a
// wiped cache directory is repaired by an ordinary refresh instead of by an
// operator procedure — and the serving path never has to write to the database
// to record what it discovered.
func (s *Store) Exists(relPath string) bool {
	full, err := s.resolve(relPath)
	if err != nil {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.Mode().IsRegular()
}

// Sweep removes leftover temporary files and reports how many it deleted.
//
// A backstop, not the mechanism: Save removes its own temporary file on every
// failure path. This catches the one case that cannot — the process dying
// mid-download — which would otherwise accumulate a file per interrupted pass
// forever, since each carries a random suffix and so never gets overwritten.
func (s *Store) Sweep() (int, error) {
	removed := 0
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A cache directory that does not exist yet is not an error; there
			// is simply nothing to sweep.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), tmpPrefix) {
			return nil
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("sweeping partial image downloads: %w", err)
	}
	return removed, nil
}

// dir returns the directory holding one item's images.
//
// Sharded on the first two hex characters of the item id, so a large library
// spreads over 256 directories rather than putting one entry per film in a
// single directory. An item's images sit together under it, which makes the
// layout legible to a person looking at the cache with ls.
func (s *Store) dir(itemID uuid.UUID) string {
	id := itemID.String()
	return filepath.Join(s.root, id[:2], id)
}

// relative turns an absolute path back into the form stored in the database:
// relative to the CACHE directory, not to the store's own root, so a row is
// readable without knowing this package's layout.
func (s *Store) relative(full string) string {
	rel, err := filepath.Rel(filepath.Dir(s.root), full)
	if err != nil {
		return full
	}
	return filepath.ToSlash(rel)
}

// resolve turns a stored relative path into an absolute one, refusing anything
// that escapes the store.
func (s *Store) resolve(relPath string) (string, error) {
	if relPath == "" {
		return "", errors.New("empty image path")
	}
	base := filepath.Dir(s.root)
	full := filepath.Join(base, filepath.FromSlash(relPath))
	if full != s.root && !strings.HasPrefix(full, s.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("image path %q escapes the image store", relPath)
	}
	return full, nil
}

// syncDir fsyncs a directory so a rename into it is durable. Best effort: see
// the caller for why a failure is not fatal.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

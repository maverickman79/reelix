package artwork

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uuid"
)

// failingReader yields some bytes and then fails, which is how a download that
// dies halfway is reproduced without killing the process.
type failingReader struct {
	prefix string
	err    error
	read   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, r.prefix), nil
	}
	return 0, r.err
}

// stalledReader yields some bytes, announces that it has, and then blocks until
// it is released. It is how a write IN PROGRESS is observed from another
// goroutine, which is the only way to see the property the rename provides.
type stalledReader struct {
	prefix   string
	written  chan struct{}
	release  chan struct{}
	finished bool
}

func newStalledReader(prefix string) *stalledReader {
	return &stalledReader{
		prefix:  prefix,
		written: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *stalledReader) Read(p []byte) (int, error) {
	if r.finished {
		return 0, io.EOF
	}
	n := copy(p, r.prefix)
	close(r.written)
	<-r.release
	r.finished = true
	return n, nil
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// TestSaveNeverExposesAPartialFile is the test this whole slice turns on, and
// it is written to fail if the ATOMIC RENAME is removed — nothing else.
//
// A HALF-WRITTEN JPEG IS WORSE THAN AN ABSENT ONE: it decodes to a half-drawn
// poster rather than to an error, so nothing downstream can tell it went wrong.
//
// The observation has to be made WHILE a write is in progress. An earlier
// version of this test asserted instead that a FAILED download leaves nothing
// under the served name — and fault injection showed that assertion cannot
// see the rename at all: writing straight to the final name still passed it,
// because the failure path's os.Remove deletes that same name. Two mechanisms,
// the rename and the cleanup, guaranteeing one asserted outcome, so removing
// either alone changed nothing observable. That is the redundant-enforcement
// pattern in a TEST rather than in code, and the fix is the same one: assert
// the property only this mechanism provides, and let the other keep its own
// test below.
func TestSaveNeverExposesAPartialFile(t *testing.T) {
	s := newStore(t)
	id := uuid.NewV7()
	served := "images/" + id.String()[:2] + "/" + id.String() + "/primary.jpg"

	reader := newStalledReader("\xff\xd8\xff\xe0 the first bytes of a jpeg")
	done := make(chan error, 1)
	go func() {
		_, err := s.Save(id, "primary", "image/jpeg", reader)
		done <- err
	}()

	// Bytes are now on disk somewhere, and the write has not finished.
	<-reader.written

	if s.Exists(served) {
		t.Error("a partially written image is visible under the name the serving path reads")
	}

	close(reader.release)
	if err := <-done; err != nil {
		t.Fatalf("saving: %v", err)
	}

	// And once it completes, it appears — all of it at once.
	if !s.Exists(served) {
		t.Fatal("the completed image is not under the served name")
	}
	f, _, err := s.Open(served)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer f.Close()
	body, _ := io.ReadAll(f)
	if string(body) != "\xff\xd8\xff\xe0 the first bytes of a jpeg" {
		t.Errorf("served %q, want the whole image", body)
	}
}

// TestAFailedSaveLeavesNothingBehind covers the OTHER mechanism: the cleanup on
// the failure path.
//
// Separate from the test above on purpose. This one is about tidiness — Save
// removing its own temporary file so the sweep stays a backstop rather than
// becoming load-bearing — and the rename is about visibility. One test each, so
// each can fail on its own.
func TestAFailedSaveLeavesNothingBehind(t *testing.T) {
	s := newStore(t)

	_, err := s.Save(uuid.NewV7(), "primary", "image/jpeg",
		&failingReader{prefix: "\xff\xd8\xff\xe0 the first bytes of a jpeg",
			err: errors.New("connection reset")})
	if err == nil {
		t.Fatal("expected the interrupted download to fail")
	}

	var found []string
	filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("the failed download left files behind: %v", found)
	}
}

// TestTagIsContentAddressed pins the contract the recorded reference tags show:
// 32 lowercase hex, stable for identical bytes, different for different bytes.
//
// The stability is what makes "re-running the pass downloads nothing" possible,
// and the difference is what makes cache-busting correct without anyone
// remembering to bump a version.
func TestTagIsContentAddressed(t *testing.T) {
	s := newStore(t)

	first, err := s.Save(uuid.NewV7(), "primary", "image/jpeg", strings.NewReader("poster bytes"))
	if err != nil {
		t.Fatalf("saving: %v", err)
	}

	if len(first.Tag) != 32 {
		t.Errorf("tag %q is %d characters, want 32", first.Tag, len(first.Tag))
	}
	if strings.Trim(first.Tag, "0123456789abcdef") != "" {
		t.Errorf("tag %q is not lowercase hex", first.Tag)
	}

	same, err := s.Save(uuid.NewV7(), "primary", "image/jpeg", strings.NewReader("poster bytes"))
	if err != nil {
		t.Fatalf("saving: %v", err)
	}
	if same.Tag != first.Tag {
		t.Errorf("identical bytes produced different tags: %s and %s", first.Tag, same.Tag)
	}

	other, err := s.Save(uuid.NewV7(), "primary", "image/jpeg", strings.NewReader("different bytes"))
	if err != nil {
		t.Fatalf("saving: %v", err)
	}
	if other.Tag == first.Tag {
		t.Error("different bytes produced the same tag, so a changed image would never cache-bust")
	}
}

// TestSaveRefusesWhatIsNotAnImage guards the failure that is expensive to
// diagnose: a CDN error page stored as a poster and served as image/jpeg looks
// like a corrupt image rather than like a failed download.
func TestSaveRefusesWhatIsNotAnImage(t *testing.T) {
	s := newStore(t)

	_, err := s.Save(uuid.NewV7(), "primary", "text/html; charset=utf-8",
		strings.NewReader("<html>404 Not Found</html>"))
	if err == nil {
		t.Fatal("expected an HTML response to be refused")
	}
}

// TestSaveEnforcesTheSizeCap bounds what one broken response can write, which
// matters because this is the first slice where a failed fetch leaves bytes on
// disk rather than an absent row.
func TestSaveEnforcesTheSizeCap(t *testing.T) {
	s := newStore(t)
	s.MaxBytes = 64

	_, err := s.Save(uuid.NewV7(), "primary", "image/jpeg", strings.NewReader(strings.Repeat("x", 65)))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}

	// Exactly at the cap is allowed: the reader deliberately looks one byte
	// past it so that hitting the limit is not mistaken for exceeding it.
	if _, err := s.Save(uuid.NewV7(), "primary", "image/jpeg",
		strings.NewReader(strings.Repeat("x", 64))); err != nil {
		t.Errorf("a file exactly at the cap was refused: %v", err)
	}
}

// TestOpenRefusesPathsOutsideTheStore treats a database row as what it is: an
// input, not a trusted path source.
func TestOpenRefusesPathsOutsideTheStore(t *testing.T) {
	s := newStore(t)

	for _, bad := range []string{
		"../../../etc/passwd",
		"images/../../etc/passwd",
		"",
	} {
		if _, _, err := s.Open(bad); err == nil {
			t.Errorf("Open(%q) succeeded; it must not read outside the store", bad)
		}
		if s.Exists(bad) {
			t.Errorf("Exists(%q) reported true", bad)
		}
	}
}

// TestSaveThenOpenRoundTrips checks the ordinary path, including that the
// stored path is relative to the CACHE directory rather than to the store's
// own root — a row has to be readable without knowing this package's layout.
func TestSaveThenOpenRoundTrips(t *testing.T) {
	cacheDir := t.TempDir()
	s := NewStore(cacheDir)
	id := uuid.NewV7()

	saved, err := s.Save(id, "backdrop", "image/jpeg", strings.NewReader("backdrop bytes"))
	if err != nil {
		t.Fatalf("saving: %v", err)
	}

	if !strings.HasPrefix(saved.Path, "images/") {
		t.Errorf("stored path %q is not relative to the cache directory", saved.Path)
	}
	if filepath.IsAbs(saved.Path) {
		t.Errorf("stored path %q is absolute", saved.Path)
	}
	if !s.Exists(saved.Path) {
		t.Fatalf("Exists(%q) is false straight after saving it", saved.Path)
	}

	f, info, err := s.Open(saved.Path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer f.Close()

	body, _ := io.ReadAll(f)
	if string(body) != "backdrop bytes" {
		t.Errorf("read %q, want the stored bytes", body)
	}
	if info.Size() != saved.Bytes {
		t.Errorf("size %d, want %d", info.Size(), saved.Bytes)
	}
}

// TestExistsIsFalseAfterAWipe is the check that makes storing bytes under
// /cache defensible rather than merely convenient. The refresh pass calls this
// for every row it thinks it has, so a cleared cache directory becomes ordinary
// work rather than an operator procedure.
func TestExistsIsFalseAfterAWipe(t *testing.T) {
	cacheDir := t.TempDir()
	s := NewStore(cacheDir)

	saved, err := s.Save(uuid.NewV7(), "primary", "image/jpeg", strings.NewReader("poster"))
	if err != nil {
		t.Fatalf("saving: %v", err)
	}
	if !s.Exists(saved.Path) {
		t.Fatal("the image is missing straight after saving it")
	}

	if err := os.RemoveAll(filepath.Join(cacheDir, "images")); err != nil {
		t.Fatalf("wiping the cache: %v", err)
	}
	if s.Exists(saved.Path) {
		t.Error("Exists reported true for a wiped cache, so nothing would ever re-download")
	}
}

// TestSweepRemovesOnlyPartials checks the backstop: leftovers from a process
// that died mid-download go, and stored images stay.
func TestSweepRemovesOnlyPartials(t *testing.T) {
	s := newStore(t)
	id := uuid.NewV7()

	saved, err := s.Save(id, "primary", "image/jpeg", strings.NewReader("poster"))
	if err != nil {
		t.Fatalf("saving: %v", err)
	}

	// Two leftovers, because each carries a random suffix and so would
	// otherwise accumulate one per interrupted pass forever.
	dir := s.dir(id)
	for _, name := range []string{tmpPrefix + "primary-123", tmpPrefix + "logo-456"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("half"), 0o644); err != nil {
			t.Fatalf("writing a leftover: %v", err)
		}
	}

	removed, err := s.Sweep()
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if removed != 2 {
		t.Errorf("swept %d, want 2", removed)
	}
	if !s.Exists(saved.Path) {
		t.Error("the sweep removed a stored image")
	}
}

// TestSweepOnAnEmptyCacheIsNotAnError covers the first run of a fresh
// deployment, where the directory does not exist yet.
func TestSweepOnAnEmptyCacheIsNotAnError(t *testing.T) {
	if _, err := newStore(t).Sweep(); err != nil {
		t.Errorf("sweeping an absent cache directory failed: %v", err)
	}
}

// TestExtensionForFoldsParameters checks the content types arrive as servers
// really send them.
func TestExtensionForFoldsParameters(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"image/jpeg", ".jpg", true},
		{"IMAGE/JPEG", ".jpg", true},
		{"image/jpeg; charset=binary", ".jpg", true},
		{" image/png ", ".png", true},
		{"image/webp", ".webp", true},
		{"text/html", "", false},
		{"", "", false},
	} {
		got, ok := ExtensionFor(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ExtensionFor(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

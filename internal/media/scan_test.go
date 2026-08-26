package media

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeFile creates a file of the given size under root.
func writeFile(t *testing.T, root, rel string, size int) string {
	t.Helper()

	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// paths returns the discovered paths relative to root, for readable assertions.
func paths(t *testing.T, root string, found []DiscoveredFile) []string {
	t.Helper()

	out := make([]string, 0, len(found))
	for _, f := range found {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			t.Fatalf("relativising %s: %v", f.Path, err)
		}
		out = append(out, rel)
	}
	return out
}

// TestScanSkipsSampleDirectories is the Radarr convention: a Sample/
// subdirectory holds a short excerpt of the same film.
//
// The test library this was written against has no such directories — the
// rsync that populated it used an explicit file list. The convention is real
// and the media may gain them later, so the behaviour is covered synthetically
// rather than left untested.
//
// These files are genuine video and probe cleanly, so nothing downstream would
// notice they are not the feature. They have to be excluded here or they
// become duplicate movies.
func TestScanSkipsSampleDirectories(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "Congo.1995.BluRay.x264-OFT/Congo.1995.BluRay.x264-OFT.mkv", 2048)
	writeFile(t, root, "Congo.1995.BluRay.x264-OFT/Sample/congo-sample.mkv", 512)
	writeFile(t, root, "Idiocracy.2006.WEB-DL/Idiocracy.2006.WEB-DL.mkv", 2048)
	writeFile(t, root, "Idiocracy.2006.WEB-DL/sample/idiocracy-sample.mkv", 512)
	writeFile(t, root, "Fight.Club.1999.REMUX/SAMPLES/clip.mkv", 512)

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got := paths(t, root, found)
	want := []string{
		"Congo.1995.BluRay.x264-OFT/Congo.1995.BluRay.x264-OFT.mkv",
		"Idiocracy.2006.WEB-DL/Idiocracy.2006.WEB-DL.mkv",
	}

	if !slices.Equal(got, want) {
		t.Errorf("discovered %v, want %v", got, want)
	}

	// Checked against the relative path: the temp directory this test runs in
	// is itself named after the test, so an absolute-path check would match
	// its own scaffolding rather than the media.
	for _, rel := range got {
		if strings.Contains(strings.ToLower(rel), "sample") {
			t.Errorf("a sample file was indexed: %s", rel)
		}
	}
}

// TestScanGroupsByDirectory covers the constitution's Movie != File rule: a
// release folder holding several files is one movie.
func TestScanGroupsByDirectory(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "Kill Bill (2003)/Kill Bill (2003) - part1.mkv", 1024)
	writeFile(t, root, "Kill Bill (2003)/Kill Bill (2003) - part2.mkv", 1024)
	writeFile(t, root, "Arrival (2016).mkv", 1024)

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("discovered %d files, want 3", len(found))
	}

	bySource := make(map[string][]string)
	for _, f := range found {
		bySource[f.SourcePath] = append(bySource[f.SourcePath], f.Filename)
	}

	if len(bySource) != 2 {
		t.Fatalf("files grouped into %d movies, want 2", len(bySource))
	}

	// The two parts share one source path and therefore one movie.
	killBill := filepath.Join(root, "Kill Bill (2003)")
	if len(bySource[killBill]) != 2 {
		t.Errorf("Kill Bill has %d files, want 2", len(bySource[killBill]))
	}

	// A file directly in the root is its own movie, keyed by its own path.
	arrival := filepath.Join(root, "Arrival (2016).mkv")
	if len(bySource[arrival]) != 1 {
		t.Errorf("Arrival has %d files, want 1", len(bySource[arrival]))
	}

	for _, f := range found {
		if f.SourcePath == killBill && f.Name.Title != "Kill Bill" {
			t.Errorf("release folder title parsed as %q, want Kill Bill", f.Name.Title)
		}
		if f.SourcePath == arrival && f.Name.Title != "Arrival" {
			t.Errorf("root-level title parsed as %q, want Arrival", f.Name.Title)
		}
	}
}

// TestScanExtensionFilter checks a library directory's non-media clutter is
// left alone.
func TestScanExtensionFilter(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "Movie (2020).mkv", 1024)
	writeFile(t, root, "Movie (2020).mp4", 1024)
	writeFile(t, root, "Movie (2020).M4V", 1024) // case-insensitive
	writeFile(t, root, "Movie (2020).srt", 16)
	writeFile(t, root, "Movie (2020).nfo", 16)
	writeFile(t, root, "poster.jpg", 16)
	writeFile(t, root, "Movie (2020).mkv.partial", 16)
	writeFile(t, root, "notes.txt", 16)

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got := paths(t, root, found)
	want := []string{"Movie (2020).M4V", "Movie (2020).mkv", "Movie (2020).mp4"}

	if !slices.Equal(got, want) {
		t.Errorf("discovered %v, want %v", got, want)
	}
}

// TestScanSkipsHiddenEntries covers metadata and sync directories.
func TestScanSkipsHiddenEntries(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "Movie (2020).mkv", 1024)
	writeFile(t, root, ".stfolder/leftover.mkv", 1024)
	writeFile(t, root, ".AppleDouble/Movie (2020).mkv", 1024)
	writeFile(t, root, ".hidden.mkv", 1024)

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got := paths(t, root, found)
	if !slices.Equal(got, []string{"Movie (2020).mkv"}) {
		t.Errorf("discovered %v, want just the visible file", got)
	}
}

// TestScanAwkwardFilenames covers the shapes the real test library contains:
// spaces, brackets, parentheses, and apostrophes.
func TestScanAwkwardFilenames(t *testing.T) {
	root := t.TempDir()

	names := []string{
		"The Legend of Aang - The Last Airbender (2026) [INTERNAL] 1080p H.264 English AAC 2.0.mkv",
		"Some Movie (2011) [1080p] [x264] {edition-Director's Cut}.mkv",
		"Gangland 2025 1080p WEB-DL H264-CinemaCity.mp4",
	}
	for _, n := range names {
		writeFile(t, root, n, 1024)
	}

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != len(names) {
		t.Fatalf("discovered %d files, want %d", len(found), len(names))
	}

	for _, f := range found {
		// The raw filename is stored verbatim alongside the parsed title.
		if !slices.Contains(names, f.Filename) {
			t.Errorf("filename came back as %q", f.Filename)
		}
		if f.Name.Title == "" {
			t.Errorf("%s produced an empty title", f.Filename)
		}
	}
}

// TestScanIsDeterministic checks repeated scans return the same order, which
// is what makes progress reporting and re-scan comparison meaningful.
func TestScanIsDeterministic(t *testing.T) {
	root := t.TempDir()

	for _, n := range []string{"c.mkv", "a.mkv", "b.mkv", "d/e.mkv", "d/f.mkv"} {
		writeFile(t, root, n, 1024)
	}

	first, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	for range 5 {
		next, err := Scan(context.Background(), []string{root})
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if !slices.Equal(paths(t, root, first), paths(t, root, next)) {
			t.Fatal("repeated scans returned different orders")
		}
	}
}

// TestScanRecordsSize checks the discovered size is carried through as int64.
func TestScanRecordsSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Movie (2020).mkv", 4096)

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("discovered %d files, want 1", len(found))
	}
	if found[0].SizeBytes != 4096 {
		t.Errorf("size = %d, want 4096", found[0].SizeBytes)
	}
}

// TestScanMultipleRoots covers a library spanning several paths, which the
// schema supports even though 0.0.1 configures one.
func TestScanMultipleRoots(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	writeFile(t, a, "Movie A (2020).mkv", 1024)
	writeFile(t, b, "Movie B (2021).mkv", 1024)

	found, err := Scan(context.Background(), []string{a, b})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("discovered %d files across two roots, want 2", len(found))
	}
}

// TestScanMissingRootIsAnError checks an unreadable root fails loudly.
//
// Scanning nothing successfully would report an empty library as a healthy
// one, which is worse than an error naming the path.
func TestScanMissingRootIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	if _, err := Scan(context.Background(), []string{missing}); err == nil {
		t.Fatal("scanning a missing root returned no error")
	}
}

// TestScanRootIsNotSkippedByName checks a library root called "sample" is
// still scanned. The skip applies to subdirectories, not to what the operator
// deliberately configured.
func TestScanRootIsNotSkippedByName(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Sample")

	writeFile(t, parent, "Sample/Movie (2020).mkv", 1024)

	found, err := Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("discovered %d files in a root named Sample, want 1", len(found))
	}
}

// TestScanCancellation checks a cancelled context stops the walk.
func TestScanCancellation(t *testing.T) {
	root := t.TempDir()
	for i := range 50 {
		writeFile(t, root, string(rune('a'+i%26))+"-movie.mkv", 16)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Scan(ctx, []string{root}); err == nil {
		t.Error("scanning with a cancelled context returned no error")
	}
}

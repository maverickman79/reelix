package media

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// videoExtensions are the containers the scanner will index.
//
// Deliberately a modest allowlist rather than "anything ffprobe can open": a
// library directory also holds subtitles, artwork, NFO files, and .partial
// downloads, none of which are movies.
var videoExtensions = []string{
	".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv",
	".ts", ".m2ts", ".mpg", ".mpeg", ".webm",
}

// skipDirNames are directory names never descended into.
//
// "sample" is the Radarr and scene convention for a short excerpt stored
// alongside the feature. Those files are real video and probe cleanly, so
// nothing downstream would notice they are not the movie — they must be
// excluded here or they become duplicate entries for films the library
// already has.
//
// "extras", "featurettes", and friends are deliberately absent: 0.0.1 excludes
// special features entirely, and skipping them silently would be a different
// decision from not supporting them.
var skipDirNames = []string{"sample", "samples"}

// DiscoveredFile is one video file found on disk.
type DiscoveredFile struct {
	// Path is absolute.
	Path string
	// Filename is the base name, stored raw alongside the parsed title.
	Filename string
	// SizeBytes is the size at discovery time. int64 throughout: the test
	// library alone contains a file past 76GB.
	SizeBytes int64
	// SourcePath identifies the movie this file belongs to — the containing
	// directory, or the file's own path when it sits directly in a library
	// root. Files sharing a SourcePath are files of the same movie.
	SourcePath string
	// Name is what SourcePath's basename parsed to.
	Name ParsedName
}

// Scan walks the library roots and returns every video file found.
//
// Roots are walked in order and results are sorted by path, so a scan of an
// unchanged library produces an identical sequence every time. That
// determinism is what makes progress reporting and re-scan comparisons
// meaningful.
//
// An unreadable subdirectory is skipped rather than failing the walk: one bad
// mount point should not prevent the rest of a library from being indexed.
// An unreadable root is an error, because scanning nothing successfully is
// worse than reporting a problem.
func Scan(ctx context.Context, roots []string) ([]DiscoveredFile, error) {
	var found []DiscoveredFile

	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, &fs.PathError{Op: "scan", Path: root, Err: fs.ErrInvalid}
		}

		files, err := scanRoot(ctx, root)
		if err != nil {
			return nil, err
		}
		found = append(found, files...)
	}

	slices.SortFunc(found, func(a, b DiscoveredFile) int {
		return strings.Compare(a.Path, b.Path)
	})
	return found, nil
}

func scanRoot(ctx context.Context, root string) ([]DiscoveredFile, error) {
	var found []DiscoveredFile

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory we cannot read is skipped; a file we cannot stat is
			// ignored. Either way the rest of the library still gets indexed.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		name := d.Name()

		if d.IsDir() {
			// Never skip the root itself, whatever it happens to be called.
			if path == root {
				return nil
			}
			if shouldSkipDir(name) {
				return fs.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() || !isVideoFile(name) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		found = append(found, newDiscoveredFile(root, path, name, info.Size()))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// newDiscoveredFile decides which movie a file belongs to.
//
// A file directly inside a library root stands alone. A file inside a
// subdirectory belongs to that directory, so a release folder holding several
// parts or encodes becomes one movie with several files — the constitution is
// explicit that Movie != File.
func newDiscoveredFile(root, path, filename string, size int64) DiscoveredFile {
	dir := filepath.Dir(path)

	sourcePath := dir
	nameSource := filepath.Base(dir)

	if dir == filepath.Clean(root) {
		sourcePath = path
		nameSource = filename
	}

	return DiscoveredFile{
		Path:       path,
		Filename:   filename,
		SizeBytes:  size,
		SourcePath: sourcePath,
		Name:       ParseName(nameSource),
	}
}

// shouldSkipDir reports whether a directory should not be descended into.
func shouldSkipDir(name string) bool {
	// Dot-directories hold metadata and synchronisation state, never media:
	// .AppleDouble, .git, .stfolder, and so on.
	if strings.HasPrefix(name, ".") {
		return true
	}
	return slices.Contains(skipDirNames, strings.ToLower(name))
}

// isVideoFile reports whether a filename has a container extension Reelix
// indexes.
func isVideoFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	return slices.Contains(videoExtensions, strings.ToLower(filepath.Ext(name)))
}

package demogit

import (
	"os"

	"github.com/go-git/go-billy/v5"
)

// cloneMetadataFS bounds writes to the fresh .git directory, including the
// temporary pack streamed by go-git. Rewrites count toward the same budget.
// The pack indexer reads concurrently, while go-git serializes these writes.
type cloneMetadataFS struct {
	billy.Filesystem
	maxBytes int64
	written  int64
	files    map[*cloneMetadataFile]struct{}
}

func (fs *cloneMetadataFS) Create(path string) (billy.File, error) {
	return fs.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (fs *cloneMetadataFS) Open(path string) (billy.File, error) {
	return fs.track(fs.Filesystem.Open(path))
}

func (fs *cloneMetadataFS) OpenFile(path string, flag int, perm os.FileMode) (billy.File, error) {
	return fs.track(fs.Filesystem.OpenFile(path, flag, perm))
}

func (fs *cloneMetadataFS) TempFile(dir, prefix string) (billy.File, error) {
	return fs.track(fs.Filesystem.TempFile(dir, prefix))
}

func (fs *cloneMetadataFS) track(file billy.File, err error) (billy.File, error) {
	if err != nil {
		return nil, err
	}
	if fs.files == nil {
		fs.files = make(map[*cloneMetadataFile]struct{})
	}
	f := &cloneMetadataFile{File: file, fs: fs}
	fs.files[f] = struct{}{}
	return f, nil
}

// An incomplete pack can leave go-git's reader and writer open. Release them
// before deleting the clone so their unlinked files release their disk space.
func (fs *cloneMetadataFS) closeFiles() {
	for file := range fs.files {
		_ = file.Close()
	}
}

type cloneMetadataFile struct {
	billy.File
	fs *cloneMetadataFS
}

func (f *cloneMetadataFile) Write(p []byte) (int, error) {
	if int64(len(p)) > f.fs.maxBytes-f.fs.written {
		return 0, untrusted(".git", "size limit exceeded (%d bytes) while writing clone metadata", f.fs.maxBytes)
	}
	n, err := f.File.Write(p)
	f.fs.written += int64(n)
	return n, err
}

func (f *cloneMetadataFile) Close() error {
	delete(f.fs.files, f)
	return f.File.Close()
}

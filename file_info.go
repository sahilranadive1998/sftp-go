package sftp

import (
	"os"
	"time"
)

// MyFileInfo is a custom struct that implements the os.FileInfo interface.
type MyFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

// Name returns the base name of the file.
func (fi MyFileInfo) Name() string {
	return fi.name
}

// Size returns the length in bytes for regular files; system-dependent for others.
func (fi MyFileInfo) Size() int64 {
	return fi.size
}

// Mode returns the file mode bits.
func (fi MyFileInfo) Mode() os.FileMode {
	return fi.mode
}

// ModTime returns the modification time.
func (fi MyFileInfo) ModTime() time.Time {
	return fi.modTime
}

// IsDir returns true if the file is a directory.
func (fi MyFileInfo) IsDir() bool {
	return fi.isDir
}

// Sys returns the underlying data source (can return nil).
func (fi MyFileInfo) Sys() interface{} {
	return nil
}

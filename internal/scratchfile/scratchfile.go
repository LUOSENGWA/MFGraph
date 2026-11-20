// Package scratchfile provides seekable private storage for streaming writes.
package scratchfile

import "os"

// New unlinks the file immediately on systems that allow it. The OS then
// reclaims its space even if the process is killed before cleanup runs.
func New(pattern string) (*os.File, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		return f, func() {
			f.Close()
			os.Remove(f.Name())
		}, nil
	}
	return f, func() { f.Close() }, nil
}

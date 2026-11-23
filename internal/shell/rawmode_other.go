//go:build !darwin && !linux

package shell

import "errors"

// enableRawMode is unsupported on this platform; interactive input falls back
// to line-buffered reads.
func enableRawMode(int) (func(), error) {
	return nil, errors.New("raw terminal mode not supported on this platform")
}

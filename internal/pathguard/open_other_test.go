//go:build !unix

package pathguard

import "errors"

// makeFIFO reports that this platform has no mkfifo, so the caller skips.
func makeFIFO(string) error {
	return errors.New("no mkfifo on this platform")
}

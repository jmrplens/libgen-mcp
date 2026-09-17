//go:build !linux

package main

import "errors"

// peerStdinIsNull cannot see another process's file descriptors without procfs.
// The probe then assumes HTTP and lets the connection decide; a deployment on
// this platform gives --healthcheck its target outright.
func peerStdinIsNull(int32) (bool, error) {
	return false, errors.ErrUnsupported
}

// peerEnviron cannot see another process's environment without procfs.
//
// The probe then knows only what is on the command line, which is right for
// every deployment that passes flags and wrong for one configured through
// variables alone — so the failure is carried into the reason the probe prints
// rather than swallowed. A deployment on this platform that configures its
// listener through the environment gives --healthcheck its target outright.
func peerEnviron(int32) (map[string]string, error) {
	return nil, errors.ErrUnsupported
}

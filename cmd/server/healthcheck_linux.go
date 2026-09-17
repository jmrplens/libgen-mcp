package main

// peerStdinIsNull reads a peer's file descriptor 0 from procfs, which is what
// its --transport auto read to choose HTTP or stdio.
func peerStdinIsNull(pid int32) (bool, error) {
	return stdinIsNullUnder("/proc", pid)
}

// peerEnviron reads a peer's listener variables from procfs, which is where a
// deployment configured through `environment:` rather than a command line put
// them.
func peerEnviron(pid int32) (map[string]string, error) {
	return environUnder("/proc", pid)
}

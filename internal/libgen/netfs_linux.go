// The Linux half of networkFilesystem: which temp directories cannot hold a
// read root's lock in a way every host sharing them would see.

package libgen

import (
	"os"

	"golang.org/x/sys/unix"
)

// statfsType is filesystemType, as a seam: a test machine has no NFS or SMB
// mount to put the temp directory on, so only a substitute can make one look
// like it.
var statfsType = filesystemType

// filesystemType is the filesystem type number statfs(2) reports for dir.
//
// It is a uint32 because the field is an int64, an int32 or a uint32
// depending on the architecture, and every type number is 32 bits wide.
func filesystemType(dir string) (uint32, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, &os.PathError{Op: "statfs", Path: dir, Err: err}
	}
	return uint32(st.Type), nil //nolint:gosec // G115: a filesystem type number is 32 bits on every architecture; the wider field only carries it
}

// networkFilesystem names the network filesystem dir is on, or answers ""
// when it is on any other.
//
// The ones it names are those where Linux keeps a lock on a directory to the
// host that took it, whatever the mount options. flock(2) hands a lock to the
// filesystem only when the open file's operations provide one, and the
// directory operations of the NFS, SMB, FUSE, 9p and AFS clients do not, so
// the kernel takes a local lock instead, which no other host sees. (A regular
// file on NFS or SMB is locked on the server, which is why a lock on a file
// inside the root was coherent there and a lock on the root itself is not.)
// Another host's sweep would then find a live root's lock free and remove the
// root from under its owner, so a temp directory on one of these makes no read
// root at all (see readRoot.unlocked).
//
// FUSE is named whatever is behind it, because sshfs, virtiofs and the like
// share a directory between machines and the kernel cannot tell those from a
// FUSE filesystem on local disk. Ceph, GFS2 and OCFS2 are not named: their
// directories take a lock that is coherent across the cluster.
func networkFilesystem(dir string) (string, error) {
	magic, err := statfsType(dir)
	if err != nil {
		return "", err
	}
	return networkFilesystemName(magic), nil
}

// networkFilesystemName names the filesystem type number magic when it is one
// networkFilesystem refuses, and answers "" for any other.
func networkFilesystemName(magic uint32) string {
	switch magic {
	case unix.NFS_SUPER_MAGIC:
		return "NFS"
	case unix.CIFS_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC, unix.SMB_SUPER_MAGIC:
		return "SMB"
	case unix.FUSE_SUPER_MAGIC:
		return "FUSE"
	case unix.V9FS_MAGIC:
		return "9p"
	case unix.AFS_SUPER_MAGIC, unix.AFS_FS_MAGIC:
		return "AFS"
	}
	return ""
}

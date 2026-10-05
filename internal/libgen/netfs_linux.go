// The Linux half of networkFilesystem: which temp directories cannot hold a
// read root's lock in a way every host sharing them would see.

package libgen

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// statfsType is filesystemType, as a seam: a test machine has no NFS or SMB
// mount to put the temp directory on, so only a substitute can make one look
// like it.
var statfsType = filesystemType

// The type numbers statfs(2) reports for two filesystems networkFilesystem
// refuses and golang.org/x/sys/unix does not name, as the kernel defines them
// (fs/orangefs/protocol.h, fs/vboxsf/super.c).
const (
	orangefsSuperMagic = 0x20030528
	vboxsfSuperMagic   = 0x786f4256
)

// mountInfoPath is the mount table of this process's mount namespace, as a
// seam for the same reason: no test machine has a 9p mount for the temp
// directory to be on, so only a substitute table can say it is.
var mountInfoPath = "/proc/self/mountinfo"

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
// directory operations of the NFS, SMB, FUSE, 9p, AFS, Coda, OrangeFS and
// VirtualBox shared folder clients do not, so the kernel takes a local lock
// instead, which no other host sees. (A regular file on NFS or SMB is locked
// on the server, which is why a lock on a file inside the root was coherent
// there and a lock on the root itself is not.) Another host's sweep would then
// find a live root's lock free and remove the root from under its owner, so a
// temp directory on one of these makes no read root at all (see
// readRoot.unlocked).
//
// FUSE is named whatever is behind it, because sshfs, virtiofs and the like
// share a directory between machines and the kernel cannot tell those from a
// FUSE filesystem on local disk. Ceph, GFS2 and OCFS2 are not named: their
// directories take a lock that is coherent across the cluster, unless GFS2 or
// OCFS2 is mounted localflocks, which is the operator asking for every flock
// to stay on its node. That option is not read off the mount table, because
// GFS2 sets it on every mount without a cluster lock manager (lock_nolock),
// which only one host can mount, and refusing those would cost a single host
// its sweep for nothing. The configuration reference says not to share a temp
// directory between hosts on a localflocks mount instead.
//
// statfs names every one of them by its own type number but one: a 9p mount
// speaking 9P2000.L, which is the Linux default, reports the type of the
// filesystem behind the share on the server (ext4 under a QEMU virtfs share
// of an ext4 directory), so statfs alone takes it for a local disk. That one
// is known by the type the mount table gives the mount instead (see
// mountType).
func networkFilesystem(dir string) (string, error) {
	magic, err := statfsType(dir)
	if err != nil {
		return "", err
	}
	if name := networkFilesystemName(magic); name != "" {
		return name, nil
	}
	fstype, err := mountType(dir)
	if err != nil {
		return "", err
	}
	if fstype == "9p" {
		return "9p", nil
	}
	return "", nil
}

// mountType is the filesystem type of the mount dir is on, as the mount table
// names it (the name mount -t takes), "" when the table lists no mount of
// dir's device, and errMountTableUnreadable when the table cannot be read.
//
// The mount is found by the device number its files report. A filesystem
// whose files report another device number than its mount (a Btrfs
// subvolume) is not listed, and leaves statfs's answer standing. So does a
// mount outside a chroot, which the kernel leaves out of the table: a chroot
// whose root is on a 9P2000.L share is not recognized. A 9p mount's files
// report its own device.
func mountType(dir string) (string, error) {
	dev, err := deviceNumber(dir)
	if err != nil {
		return "", nil
	}
	table, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return "", errMountTableUnreadable
	}
	for line := range strings.Lines(string(table)) {
		if lineDev, fstype := mountEntry(line); lineDev == dev {
			return fstype, nil
		}
	}
	return "", nil
}

// deviceNumber is the device number of the filesystem dir is on, written as
// the mount table writes it, major:minor.
func deviceNumber(dir string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		return "", err
	}
	dev := uint64(st.Dev) //nolint:unconvert // st.Dev is a uint32 on 32-bit MIPS
	return fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev)), nil
}

// mountEntry reads the device number and the filesystem type off one line of
// a mountinfo table (proc_pid_mountinfo(5)). The device is the third field,
// and the type is the first after the lone "-" that ends the optional fields,
// whose number varies. A line it cannot read answers "" for both, which no
// device number equals.
//
// The separator is found as " - " in the whole line, which only it can be:
// the fields before it that hold a path escape a space as \040.
func mountEntry(line string) (dev, fstype string) {
	before, after, _ := strings.Cut(line, " - ")
	head, tail := strings.Fields(before), strings.Fields(after)
	if len(head) < 3 || len(tail) == 0 {
		return "", ""
	}
	return head[2], tail[0]
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
	case unix.CODA_SUPER_MAGIC:
		return "Coda"
	case orangefsSuperMagic:
		return "OrangeFS"
	case vboxsfSuperMagic:
		return "a VirtualBox shared folder"
	}
	return ""
}

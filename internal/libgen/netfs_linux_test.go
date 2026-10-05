package libgen

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

// statfsAnswers makes every statfs this process asks answer magic and err,
// and returns a count of the questions.
func statfsAnswers(t *testing.T, magic uint32, err error) *atomic.Int32 {
	t.Helper()
	var asked atomic.Int32
	prev := statfsType
	t.Cleanup(func() { statfsType = prev })
	statfsType = func(string) (uint32, error) {
		asked.Add(1)
		return magic, err
	}
	return &asked
}

// mountTable makes the mount table this process reads hold lines, in order.
func mountTable(t *testing.T, lines ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := mountInfoPath
	t.Cleanup(func() { mountInfoPath = prev })
	mountInfoPath = path
}

// mountLine is one line of a mount table, written as the kernel writes one,
// for a mount of type fstype whose files report the device number dev.
func mountLine(dev, fstype string) string {
	return "36 25 " + dev + " / /mnt/share rw,relatime shared:1 - " + fstype + " share rw"
}

// deviceOf is the device number of a directory a test made, which has one.
func deviceOf(t *testing.T, dir string) string {
	t.Helper()
	dev, err := deviceNumber(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dev
}

// otherDevice is a device number no directory a test makes is on.
const otherDevice = "0:999999"

// TestNetworkFilesystemName names exactly the filesystems whose directories
// Linux locks on this host alone, and none whose directories take a lock that
// reaches every host (Ceph, GFS2, OCFS2) or that only one host mounts.
// OrangeFS, VirtualBox's shared folders and GFS2 are written as the kernel's
// own numbers, because x/sys/unix names none of them and a test spelling the
// constant it tests could not catch a wrong one.
func TestNetworkFilesystemName(t *testing.T) {
	cases := []struct {
		name  string
		magic uint32
		want  string
	}{
		{"NFS", unix.NFS_SUPER_MAGIC, "NFS"},
		{"CIFS", unix.CIFS_SUPER_MAGIC, "SMB"},
		{"SMB2", unix.SMB2_SUPER_MAGIC, "SMB"},
		{"smbfs", unix.SMB_SUPER_MAGIC, "SMB"},
		{"FUSE", unix.FUSE_SUPER_MAGIC, "FUSE"},
		{"9p", unix.V9FS_MAGIC, "9p"},
		{"AFS", unix.AFS_SUPER_MAGIC, "AFS"},
		{"kAFS", unix.AFS_FS_MAGIC, "AFS"},
		{"Coda", unix.CODA_SUPER_MAGIC, "Coda"},
		{"OrangeFS", 0x20030528, "OrangeFS"},
		{"vboxsf", 0x786f4256, "a VirtualBox shared folder"},
		{"ext4", unix.EXT4_SUPER_MAGIC, ""},
		{"tmpfs", unix.TMPFS_MAGIC, ""},
		{"XFS", unix.XFS_SUPER_MAGIC, ""},
		{"Btrfs", unix.BTRFS_SUPER_MAGIC, ""},
		{"overlayfs", unix.OVERLAYFS_SUPER_MAGIC, ""},
		{"Ceph", unix.CEPH_SUPER_MAGIC, ""},
		{"GFS2", 0x01161970, ""},
		{"OCFS2", unix.OCFS2_SUPER_MAGIC, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := networkFilesystemName(tc.magic); got != tc.want {
				t.Errorf("networkFilesystemName(%#x) = %q, want %q", tc.magic, got, tc.want)
			}
		})
	}
}

// TestFilesystemType asks the kernel for real: a directory that exists has a
// type, and one that does not is an error naming the call and the path, as an
// os function's would.
func TestFilesystemType(t *testing.T) {
	t.Run("an existing directory", func(t *testing.T) {
		magic, err := filesystemType(t.TempDir())
		if err != nil || magic == 0 {
			t.Errorf("filesystemType = %#x, %v, want a type and no error", magic, err)
		}
	})
	t.Run("a missing directory", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent")
		_, err := filesystemType(missing)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != "statfs" || pathErr.Path != missing || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("filesystemType error = %v, want a statfs *os.PathError for %q that is fs.ErrNotExist", err, missing)
		}
	})
}

// TestNetworkFilesystem asks statfs first and the mount table only when
// statfs names nothing: a type statfs names, a 9p mount statfs takes for the
// disk behind it, a local filesystem by both, and a statfs that fails.
func TestNetworkFilesystem(t *testing.T) {
	statfsFailed := errors.New("statfs failed")
	cases := []struct {
		name    string
		magic   uint32
		err     error
		mounted string
		want    string
		wantErr error
	}{
		{"a network filesystem", unix.NFS_SUPER_MAGIC, nil, "ext4", "NFS", nil},
		{"9P2000.L over an ext4 share", unix.EXT4_SUPER_MAGIC, nil, "9p", "9p", nil},
		{"a local filesystem", unix.EXT4_SUPER_MAGIC, nil, "ext4", "", nil},
		{"statfs fails", 0, statfsFailed, "9p", "", statfsFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			statfsAnswers(t, tc.magic, tc.err)
			mountTable(t, mountLine(deviceOf(t, dir), tc.mounted))
			got, err := networkFilesystem(dir)
			if got != tc.want || !errors.Is(err, tc.wantErr) {
				t.Errorf("networkFilesystem = %q, %v, want %q, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// TestMountType finds the mount of a directory's device in the table, past a
// line for another device, answers "" for a device the table does not list or
// a directory that is not there, and errMountTableUnreadable for a table that
// cannot be read. The first case reads the kernel's own table, where /proc is
// a proc mount on every Linux system.
func TestMountType(t *testing.T) {
	t.Run("the kernel's own table", func(t *testing.T) {
		if got, err := mountType("/proc"); got != "proc" || err != nil {
			t.Errorf("mountType(/proc) = %q, %v, want proc, nil", got, err)
		}
	})
	t.Run("the table cannot be read", func(t *testing.T) {
		prev := mountInfoPath
		t.Cleanup(func() { mountInfoPath = prev })
		mountInfoPath = filepath.Join(t.TempDir(), "absent")
		if got, err := mountType(t.TempDir()); got != "" || !errors.Is(err, errMountTableUnreadable) {
			t.Errorf("mountType = %q, %v, want \"\", errMountTableUnreadable", got, err)
		}
		if got, err := networkFilesystem(t.TempDir()); got != "" || !errors.Is(err, errMountTableUnreadable) {
			t.Errorf("networkFilesystem = %q, %v, want \"\", errMountTableUnreadable", got, err)
		}
		if f, err := holdReadRootLock(t.TempDir()); f != nil || !errors.Is(err, errLocksUnusable) {
			t.Errorf("holdReadRootLock = %v, %v, want nil, errLocksUnusable", f, err)
		}
	})
	cases := []struct {
		name    string
		table   func(dev string) []string
		missing bool
		want    string
	}{
		{"the device is listed after another", func(dev string) []string {
			return []string{mountLine(otherDevice, "nfs4"), mountLine(dev, "9p")}
		}, false, "9p"},
		{"the device is not listed", func(string) []string {
			return []string{mountLine(otherDevice, "9p")}
		}, false, ""},
		{"the directory is not there", func(dev string) []string {
			return []string{mountLine(dev, "9p")}
		}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mountTable(t, tc.table(deviceOf(t, dir))...)
			if tc.missing {
				dir = filepath.Join(dir, "absent")
			}
			if got, err := mountType(dir); got != tc.want || err != nil {
				t.Errorf("mountType = %q, %v, want %q, nil", got, err, tc.want)
			}
		})
	}
}

// TestDeviceNumber_AMissingDirectory fails for a directory that is not there.
// That a device number is written as the mount table writes it is
// TestMountType's first case, which matches one against the kernel's table.
func TestDeviceNumber_AMissingDirectory(t *testing.T) {
	if dev, err := deviceNumber(filepath.Join(t.TempDir(), "absent")); dev != "" || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("deviceNumber = %q, %v, want \"\" and fs.ErrNotExist", dev, err)
	}
}

// TestMountEntry reads the device number and the type off every shape of
// mountinfo line, wherever the separator falls, and nothing off a line that
// lacks either.
func TestMountEntry(t *testing.T) {
	cases := []struct {
		name, line, dev, fstype string
	}{
		{"one optional field", "29 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro\n", "8:1", "ext4"},
		{"no optional field", "40 29 0:35 / /mnt/share rw,relatime - 9p hostshare rw,access=client,trans=virtio", "0:35", "9p"},
		{"several optional fields", "41 29 0:36 / /mnt/a rw shared:5 master:2 propagate_from:1 - nfs4 srv:/x rw", "0:36", "nfs4"},
		{"a mount point holding a dash between spaces", `42 29 0:37 / /mnt/a\040-\040b rw - tmpfs tmpfs rw`, "0:37", "tmpfs"},
		{"the fewest fields that hold both", "1 2 0:5 - 9p", "0:5", "9p"},
		{"too few fields before the separator", "1 0:5 - 9p", "", ""},
		{"no separator", "29 1 8:1 / / rw ext4 /dev/sda1 rw", "", ""},
		{"nothing after the separator", "29 1 8:1 / / rw - \n", "", ""},
		{"an empty line", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if dev, fstype := mountEntry(tc.line); dev != tc.dev || fstype != tc.fstype {
				t.Errorf("mountEntry(%q) = %q, %q, want %q, %q", tc.line, dev, fstype, tc.dev, tc.fstype)
			}
		})
	}
}

// TestHoldReadRootLock_OnANetworkFilesystem refuses a root whose lock only
// this host would see, before any lock is tried, as errLocksUnusable: the
// lock would be taken without a word, and only another host's sweep would
// find out it never saw it. A statfs that fails is an ordinary error, which
// does not make a process give up on read roots.
func TestHoldReadRootLock_OnANetworkFilesystem(t *testing.T) {
	tmp := t.TempDir()
	t.Run("a network filesystem", func(t *testing.T) {
		statfsAnswers(t, unix.NFS_SUPER_MAGIC, nil)
		tries := lockAnswers(t, nil)
		f, err := holdReadRootLock(newRootDir(t, tmp))
		if f != nil {
			_ = f.Close()
			t.Error("returned a file for a root on NFS")
		}
		if !errors.Is(err, errLocksUnusable) || !strings.Contains(err.Error(), "NFS") {
			t.Errorf("error = %v, want errLocksUnusable naming NFS", err)
		}
		if got := tries.Load(); got != 0 {
			t.Errorf("the lock was tried %d times, want never", got)
		}
	})
	t.Run("9P2000.L, which only the mount table names", func(t *testing.T) {
		statfsAnswers(t, unix.EXT4_SUPER_MAGIC, nil)
		mountTable(t, mountLine(deviceOf(t, tmp), "9p"))
		tries := lockAnswers(t, nil)
		f, err := holdReadRootLock(newRootDir(t, tmp))
		if f != nil {
			_ = f.Close()
			t.Error("returned a file for a root on 9p")
		}
		if !errors.Is(err, errLocksUnusable) || !strings.Contains(err.Error(), "9p") {
			t.Errorf("error = %v, want errLocksUnusable naming 9p", err)
		}
		if got := tries.Load(); got != 0 {
			t.Errorf("the lock was tried %d times, want never", got)
		}
	})
	t.Run("statfs fails", func(t *testing.T) {
		statfsFailed := errors.New("statfs failed")
		statfsAnswers(t, 0, statfsFailed)
		f, err := holdReadRootLock(newRootDir(t, tmp))
		if f != nil {
			_ = f.Close()
			t.Error("returned a file when statfs failed")
		}
		if !errors.Is(err, statfsFailed) || errors.Is(err, errLocksUnusable) {
			t.Errorf("error = %v, want the statfs failure and not errLocksUnusable", err)
		}
	})
}

// TestReadRoot_ANetworkTempDirMakesNoRootAndSweepsNothing covers a temp
// directory on NFS, where a directory's flock stays on the host that took it.
// A root there would look dead to another host's sweep while its owner read
// from it, and this process's own sweep could do the same to a root another
// host is using, so no root is made and nothing is swept, a dead-looking root
// included: each fetch gets a loose directory, the operator is told once, and
// the filesystem is not asked again.
func TestReadRoot_ANetworkTempDirMakesNoRootAndSweepsNothing(t *testing.T) {
	tmp := t.TempDir()
	isolateTempDir(t, tmp)
	other := deadRoot(t, tmp)
	asked := statfsAnswers(t, unix.NFS_SUPER_MAGIC, nil)
	logs := captureLog(t)
	var r readRoot
	t.Cleanup(r.close)

	for _, fetch := range []string{"first", "second"} {
		t.Run(fetch, func(t *testing.T) {
			dir, err := r.fetchDir(noLoss(t))
			if err != nil {
				t.Fatalf("fetchDir: %v", err)
			}
			if filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), legacyFetchPrefix) {
				t.Errorf("fetch directory %q is not a loose %s* directory in %q", dir, legacyFetchPrefix, tmp)
			}
		})
	}
	if got := asked.Load(); got != 1 {
		t.Errorf("statfs was asked %d times, want once", got)
	}
	if roots, _ := filepath.Glob(filepath.Join(tmp, readRootPrefix+"*")); len(roots) != 1 || roots[0] != other {
		t.Errorf("read roots in the temp directory = %v, want only the one another host may be using, %q", roots, other)
	}
	if got := strings.Count(logs.String(), "will not be cleaned up automatically"); got != 1 {
		t.Errorf("the warning was logged %d times, want once: %q", got, logs.String())
	}
	if !strings.Contains(logs.String(), "NFS") {
		t.Errorf("the warning does not name the filesystem: %q", logs.String())
	}
}

// TestReadRoot_AFailedStatfsLeavesNoRootAndIsNotRemembered keeps a statfs
// that fails once from leaving the root it was asked about behind, or from
// sending the process to loose directories for good.
func TestReadRoot_AFailedStatfsLeavesNoRootAndIsNotRemembered(t *testing.T) {
	tmp := t.TempDir()
	isolateTempDir(t, tmp)
	statfsFailed := errors.New("statfs failed")
	restore := statfsType
	statfsAnswers(t, 0, statfsFailed)
	var r readRoot
	t.Cleanup(r.close)

	if _, err := r.fetchDir(noLoss(t)); !errors.Is(err, statfsFailed) {
		t.Fatalf("fetchDir error = %v, want the statfs failure", err)
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Errorf("the temp directory holds %d entries after statfs failed (%v)", len(entries), err)
	}
	statfsType = restore
	if root := rootOf(t, &r); !strings.HasPrefix(filepath.Base(root), readRootPrefix) {
		t.Errorf("the fetch after statfs recovered went under %q, want a read root", root)
	}
}

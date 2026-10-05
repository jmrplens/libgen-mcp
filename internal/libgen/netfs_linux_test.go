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

// TestNetworkFilesystemName names exactly the filesystems whose directories
// Linux locks on this host alone, and none whose directories take a lock that
// reaches every host (Ceph, OCFS2) or that only one host mounts.
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
		{"ext4", unix.EXT4_SUPER_MAGIC, ""},
		{"tmpfs", unix.TMPFS_MAGIC, ""},
		{"XFS", unix.XFS_SUPER_MAGIC, ""},
		{"Btrfs", unix.BTRFS_SUPER_MAGIC, ""},
		{"overlayfs", unix.OVERLAYFS_SUPER_MAGIC, ""},
		{"Ceph", unix.CEPH_SUPER_MAGIC, ""},
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

// TestNetworkFilesystem passes statfs's answer through: a type it names, a
// type it does not, and an error.
func TestNetworkFilesystem(t *testing.T) {
	statfsFailed := errors.New("statfs failed")
	cases := []struct {
		name    string
		magic   uint32
		err     error
		want    string
		wantErr error
	}{
		{"a network filesystem", unix.NFS_SUPER_MAGIC, nil, "NFS", nil},
		{"a local filesystem", unix.EXT4_SUPER_MAGIC, nil, "", nil},
		{"statfs fails", 0, statfsFailed, "", statfsFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statfsAnswers(t, tc.magic, tc.err)
			got, err := networkFilesystem(t.TempDir())
			if got != tc.want || !errors.Is(err, tc.wantErr) {
				t.Errorf("networkFilesystem = %q, %v, want %q, %v", got, err, tc.want, tc.wantErr)
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

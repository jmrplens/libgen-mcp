//go:build unix

package libgen

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestTryLockFile_ConflictsAcrossDescriptorsAndUnlocks pins the two properties
// the read root's sweep rests on: a second descriptor on the same directory
// cannot take a lock the first holds, even inside one process, and it can once
// the first unlocks.
func TestTryLockFile_ConflictsAcrossDescriptorsAndUnlocks(t *testing.T) {
	dir := t.TempDir()
	open := func() *os.File {
		f, err := openReadRootLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	first, second := open(), open()

	if err := tryLockFile(first); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := tryLockFile(second); !errors.Is(err, errLockHeld) {
		t.Fatalf("a second descriptor trying a lock the first holds got %v, want errLockHeld", err)
	}
	if err := unlockFile(first); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := tryLockFile(second); err != nil {
		t.Errorf("the lock stayed taken after unlock: %v", err)
	}
}

// TestTryLockFile_AnythingButHeldIsNotHeld keeps a failure to try the lock at
// all from reading as another process holding it: a descriptor that is already
// closed cannot be locked, and that says nothing about who holds what.
func TestTryLockFile_AnythingButHeldIsNotHeld(t *testing.T) {
	f, err := openReadRootLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	err = tryLockFile(f)
	if err == nil || errors.Is(err, errLockHeld) {
		t.Errorf("tryLockFile on a closed descriptor = %v, want an error that is not errLockHeld", err)
	}
}

// TestOpenReadRootLock_OpensOnlyARealDirectory is what makes it safe to open a
// name another user may have planted in a shared /tmp: a symbolic link is not
// followed, a regular file is refused, and a FIFO is refused at once rather
// than blocking the sweep until a writer appears.
func TestOpenReadRootLock_OpensOnlyARealDirectory(t *testing.T) {
	tmp := t.TempDir()
	target := t.TempDir()
	cases := []struct {
		name string
		make func(path string) error
	}{
		{"a symbolic link to a directory", func(path string) error { return os.Symlink(target, path) }},
		{"a regular file", func(path string) error { return os.WriteFile(path, nil, 0o600) }},
		{"a FIFO", func(path string) error { return unix.Mkfifo(path, 0o600) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(tmp, readRootPrefix+filepath.Base(t.Name()))
			if err := tc.make(path); err != nil {
				t.Fatal(err)
			}
			f, err := openReadRootLock(path)
			if err == nil {
				_ = f.Close()
				t.Fatalf("openReadRootLock opened %s", tc.name)
			}
		})
	}
}

// TestOwnedDirectory judges what Lstat says about a candidate: only a real
// directory owned by the user this process runs as counts.
func TestOwnedDirectory(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "dir")
	file := filepath.Join(tmp, "file")
	link := filepath.Join(tmp, "link")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		path    string
		foreign bool
		want    bool
	}{
		{"this user's directory", dir, false, true},
		{"another user's directory", dir, true, false},
		{"this user's file", file, false, false},
		{"a symbolic link to this user's directory", link, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.foreign {
				asAnotherUser(t)
			}
			info, err := os.Lstat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := ownedDirectory(info); got != tc.want {
				t.Errorf("ownedDirectory = %v, want %v", got, tc.want)
			}
		})
	}
}

// asAnotherUser makes every root this test makes look like another user's to
// the sweep, which is the only way a test can produce one without root.
func asAnotherUser(t *testing.T) {
	t.Helper()
	prev := readRootOwner
	t.Cleanup(func() { readRootOwner = prev })
	readRootOwner = func() int { return os.Geteuid() + 1 }
}

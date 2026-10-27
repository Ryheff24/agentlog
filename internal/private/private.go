// Package private creates agentlog's own files (run records, CPU claims,
// feedback) so that nobody else on the machine can read them or decide where
// they go. Some of them fall back to a shared temp directory when a sandbox
// allows nothing else, and a directory there can be made by anyone first.
package private

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotPrivate is returned for a directory or file that is a link, belongs
// to someone else, or is not what it should be.
var ErrNotPrivate = errors.New("not private to this user")

// Dir makes dir and its parents, then checks that dir itself is a real
// directory owned by this user, and takes away anything group or others could
// do with it. It only ever narrows: a directory the person made read-only
// stays that way. A directory that fails the check is never written into.
func Dir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByMe(info) {
		return fmt.Errorf("%s: %w", dir, ErrNotPrivate)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return os.Chmod(dir, perm&^0o077)
	}
	return nil
}

// Open opens a file in a directory Dir has checked, never through a link, and
// only if it is a regular file this user owns. New files are 0600, and older
// ones that group or others could read are narrowed.
func Open(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag|noFollow, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || !ownedByMe(info)) {
		err = fmt.Errorf("%s: %w", path, ErrNotPrivate)
	}
	if perm := info.Mode().Perm(); err == nil && perm&0o077 != 0 {
		err = f.Chmod(perm &^ 0o077)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

//go:build unix

package bridges

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockSlot(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func unlockSlot(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

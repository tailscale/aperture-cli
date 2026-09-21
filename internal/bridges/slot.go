package bridges

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/aperture-cli/internal/config"
)

// A Slot is one numbered node identity of a Bridge: its own state directory,
// node key and tailnet hostname. A process claims the lowest free Slot when
// its Machine first starts a node and holds the Slot's lock for as long as
// the Machine may use it. One state directory is one node key, and the
// control plane hands the node to whichever process registered last; the
// lock is what keeps two aperture processes from silently evicting each
// other's session (APT-330).
//
// Locks live outside the state directories they guard, so Destroy can remove
// a directory while its lock file is still held open — on Windows an open
// file inside the directory would make the removal fail.
const maxSlots = 100

// errSlotHeld reports a Slot another aperture process has locked.
var errSlotHeld = errors.New("slot is held by another aperture process")

// claimSlot returns the lowest free slot of the bridge and the function that
// releases it.
func claimSlot(bridgeID string) (int, func(), error) {
	for slot := range maxSlots {
		release, err := claimSlotNumber(bridgeID, slot+1)
		if errors.Is(err, errSlotHeld) {
			continue
		}
		return slot + 1, release, err
	}
	return 0, nil, fmt.Errorf("more than %d aperture processes on bridge %s", maxSlots, bridgeID)
}

// claimSlotNumber locks exactly slot, or returns errSlotHeld.
func claimSlotNumber(bridgeID string, slot int) (func(), error) {
	dir, err := config.BridgeStateDir(bridgeID, slot)
	if err != nil {
		return nil, err
	}
	suffix := strings.TrimPrefix(bridgeID, "bridge-")
	locksDir := filepath.Join(filepath.Dir(dir), "locks")
	if err := os.MkdirAll(locksDir, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(locksDir, fmt.Sprintf("%s-%d.lock", suffix, slot))
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	held, err := tryLockSlot(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if !held {
		f.Close()
		return nil, errSlotHeld
	}
	return func() {
		unlockSlot(f)
		f.Close()
	}, nil
}

package library

import (
	"fmt"
	"syscall"
)

// Volume is one disc the library is kept on, as the filesystem reports it.
//
// Schall keeps a collection on a volume with a limit, and until this there was
// no screen that said how close it was. An installation that fills its volume
// finds out from a failed import, which is the worst moment and the least clear
// message.
//
// A volume that cannot be measured says so and carries no figures. Printing a
// number nothing measured would be worse than the gap: somebody would plan
// around it.
type Volume struct {
	// Roots are the configured music folders that live on this volume, in the
	// order they were given. Several folders on one disc are one disc, and
	// counting its free space once per folder would say a library had three
	// times the room it has.
	Roots []string
	// TotalBytes is how big the volume is, and FreeBytes how much of it an
	// unprivileged writer has left. Both are zero when Measured is false.
	TotalBytes int64
	FreeBytes  int64
	Measured   bool
	// Error is why it could not be measured, in the filesystem's own words.
	Error string
}

// Volumes measures the discs a set of music folders live on, one entry per
// disc.
//
// Folders are grouped by the identifier the filesystem gives the volume, so two
// folders on one disc are reported once. A folder that cannot be read at all
// gets an entry of its own saying why, because "this folder is unreachable" is
// worth showing and is not the same fact as a full disc.
func Volumes(paths []string) []Volume {
	measured := make([]Volume, 0, len(paths))
	byVolume := make(map[syscall.Fsid]int, len(paths))
	for _, path := range paths {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			measured = append(measured, Volume{
				Roots: []string{path},
				Error: fmt.Sprintf("this folder could not be read: %v", err),
			})
			continue
		}
		if at, seen := byVolume[stat.Fsid]; seen {
			measured[at].Roots = append(measured[at].Roots, path)
			continue
		}
		byVolume[stat.Fsid] = len(measured)
		measured = append(measured, Volume{
			Roots: []string{path},
			// Bavail rather than Bfree: the blocks reserved for root are not
			// room this process has, so counting them would promise space
			// Schall cannot use.
			TotalBytes: int64(stat.Blocks) * int64(stat.Bsize),
			FreeBytes:  int64(stat.Bavail) * int64(stat.Bsize),
			Measured:   true,
		})
	}
	return measured
}

package server

import (
	"net/http"

	"github.com/pxldi/schall/internal/library"
)

// storageResponse is how much room the collection takes and how much is left.
//
// Schall keeps a collection on a volume with a limit, and nothing on any screen
// said how close it was. An installation that fills its volume finds out from a
// failed import: the worst moment, and the least clear message.
//
// A volume that could not be measured says so and carries no figures. Printing
// a number nothing measured would be worse than the gap, because somebody would
// plan around it.
type storageResponse struct {
	// CollectionBytes is what the library weighs, summed by the scan. It is the
	// same figure the library summary reports.
	CollectionBytes int64 `json:"collectionBytes"`
	// Volumes are the discs the music folders live on, one entry per disc. Two
	// folders on one disc are one entry: counting its free space twice would
	// say the library had twice the room it has.
	Volumes []storageVolumeResponse `json:"volumes"`
}

type storageVolumeResponse struct {
	Roots      []string `json:"roots"`
	TotalBytes int64    `json:"totalBytes"`
	FreeBytes  int64    `json:"freeBytes"`
	Measured   bool     `json:"measured"`
	Error      string   `json:"error,omitempty"`
}

// storage reports what the collection weighs and what the discs holding it have
// left.
//
// Both halves are read where they are rather than kept: the weight is the
// scan's own sum, and the free space is asked of the filesystem at the moment
// somebody looks. A stored figure would be wrong the first time anything else
// on the machine wrote a file.
//
// Only enabled folders are measured. A folder switched off is not being filled.
func (api *API) storage(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	summary, err := api.library.LibrarySummary(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	roots, err := api.library.ListLibraryRoots(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		if root.Enabled {
			paths = append(paths, root.Path)
		}
	}

	result := storageResponse{
		CollectionBytes: summary.TotalSizeBytes,
		Volumes:         make([]storageVolumeResponse, 0, len(paths)),
	}
	for _, volume := range library.Volumes(paths) {
		result.Volumes = append(result.Volumes, storageVolumeResponse{
			Roots: volume.Roots, TotalBytes: volume.TotalBytes,
			FreeBytes: volume.FreeBytes, Measured: volume.Measured, Error: volume.Error,
		})
	}
	api.writeJSON(response, http.StatusOK, result)
}

package server

import (
	"strconv"
	"sync"

	"github.com/pxldi/schall/internal/coverart"
)

// thumbnailWidths are the sizes a cover can be asked for with ?size=. Lists
// draw covers at 40 to 100 CSS pixels, so 128 and 256 cover them on ordinary
// and doubled screens. Anything else is served at the stored size.
var thumbnailWidths = map[int]bool{128: true, 256: true}

// maxThumbnails bounds the memory the made thumbnails take: at about 5 to 15
// KB each, a few thousand is a few tens of megabytes.
const maxThumbnails = 4096

// thumbnails keeps the scaled covers this process has made, keyed by the
// stored picture's hash and the width. A picture that changes has a new hash,
// so nothing here goes stale.
type thumbnails struct {
	mutex   sync.Mutex
	entries map[string][]byte
}

func newThumbnails() *thumbnails {
	return &thumbnails{entries: make(map[string][]byte)}
}

// thumbnailWidth reads ?size= and answers 0 when it is not a size served.
func thumbnailWidth(raw string) int {
	width, err := strconv.Atoi(raw)
	if err != nil || !thumbnailWidths[width] {
		return 0
	}
	return width
}

// scaled answers the picture at width, making it on the first ask. A picture
// that does not decode is answered as it is stored.
func (cache *thumbnails) scaled(hash string, image []byte, width int) ([]byte, bool) {
	key := hash + "/" + strconv.Itoa(width)
	cache.mutex.Lock()
	made, ok := cache.entries[key]
	cache.mutex.Unlock()
	if ok {
		return made, true
	}
	made, err := coverart.Thumbnail(image, width)
	if err != nil {
		return nil, false
	}
	cache.mutex.Lock()
	if len(cache.entries) >= maxThumbnails {
		// Any entry will do: a dropped one is made again on its next ask.
		for dropped := range cache.entries {
			delete(cache.entries, dropped)
			break
		}
	}
	cache.entries[key] = made
	cache.mutex.Unlock()
	return made, true
}

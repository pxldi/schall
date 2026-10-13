package coverart

const (
	// placeholderGrey is how far apart the three channels of a pixel may be
	// for the pixel to count as grey. JPEG noise keeps a flat grey a few
	// steps off in each channel.
	placeholderGrey = 12
	// placeholderFloor is the darkest a pixel of a stock avatar gets. The
	// light grey silhouette a provider serves for "no photo" spans 184 to 248;
	// a black and white photograph of a person has shadows far below this.
	placeholderFloor = 150
)

// Placeholder says whether a picture is a provider's stock "no photo" avatar:
// grey everywhere and light everywhere. Deezer serves one under addresses the
// blank-hash check does not know, and on a dark page of artists it showed as a
// glaring white square where the generated art should have been.
//
// It reads only display pictures. Nothing it says reaches matching or
// identity.
func Placeholder(picture []byte) bool {
	decoded, err := decode(picture)
	if err != nil {
		return false
	}
	pixels := samplePixels(decoded)
	if len(pixels) == 0 {
		return false
	}
	for _, pixel := range pixels {
		low := min(pixel[0], pixel[1], pixel[2])
		high := max(pixel[0], pixel[1], pixel[2])
		if high-low > placeholderGrey || low < placeholderFloor {
			return false
		}
	}
	return true
}

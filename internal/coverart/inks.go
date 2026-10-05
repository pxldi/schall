package coverart

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/image/draw"
)

// Inks are the two colours a page is printed in, read from a release's cover,
// and the colours the cover is made of. Display only: nothing that matches or
// imports music reads them.
type Inks struct {
	// Dark carries the chrome. It is clamped to a relative luminance of at most
	// 0.035, so light text reads on it whatever the cover was.
	Dark string
	// Light is the cover's most saturated colour, or its light cluster when no
	// colour on it is saturated. It is clamped to a relative luminance of at
	// least 0.42 so it reads on Dark and on the neutral ground.
	Light string
	// Palette is the cover's colour clusters, most common first, unclamped.
	Palette []string
}

const (
	// inkSample is the side the cover is shrunk to before it is read. Clusters
	// of a 48 by 48 picture are the clusters of the full one for this purpose,
	// and 2,304 pixels keep a backfill of thousands of covers to seconds.
	inkSample = 48
	// inkClusters is how many colours a cover is reduced to.
	inkClusters = 6
	// inkRounds bounds k-means. It settles well before this on real covers.
	inkRounds = 16
	// mainShare is the share of the cover a cluster needs to count as one of
	// its main colours, which the dark ink and the light cluster are chosen
	// from. Below it a cluster is a detail.
	mainShare = 0.08
	// accentShare is the share a cluster needs to be the accent at all, so a
	// few stray pixels of a logo do not colour a whole page.
	accentShare = 0.02
)

// ReadInks reads the inks of a cover. It reports false for a picture that does
// not decode, which includes SVG.
func ReadInks(picture []byte) (Inks, bool) {
	decoded, _, err := image.Decode(bytes.NewReader(picture))
	if err != nil {
		return Inks{}, false
	}
	pixels := samplePixels(decoded)
	if len(pixels) == 0 {
		return Inks{}, false
	}
	clusters := kmeans(pixels, inkClusters)

	palette := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		if hex := cluster.colour.hex(); !slices.Contains(palette, hex) {
			palette = append(palette, hex)
		}
	}

	main := make([]cluster, 0, len(clusters))
	for _, c := range clusters {
		if c.share >= mainShare {
			main = append(main, c)
		}
	}
	if len(main) == 0 {
		main = clusters
	}
	darkest, lightest := main[0].colour, main[0].colour
	for _, c := range main[1:] {
		if c.colour.luminance() < darkest.luminance() {
			darkest = c.colour
		}
		if c.colour.luminance() > lightest.luminance() {
			lightest = c.colour
		}
	}

	// The light ink is the cover's most saturated colour, so a black sleeve
	// with a gold title prints in gold. 0.35 saturation is where a colour stops
	// reading as grey; 0.22 to 0.9 lightness keeps out near-black and near-white.
	light := lightest
	best := -1.0
	for _, c := range clusters {
		if c.share < accentShare {
			continue
		}
		saturation, lightness := c.colour.saturation(), c.colour.lightness()
		if saturation >= 0.35 && lightness >= 0.22 && lightness <= 0.9 && saturation > best {
			best, light = saturation, c.colour
		}
	}

	dark, light := clampInks(darkest, light)
	return Inks{Dark: dark.hex(), Light: light.hex(), Palette: palette}, true
}

// InkColumns reads a cover's inks into the columns SaveReleaseCoverArt writes.
// A row with no picture gets NULL everywhere; a picture that does not decode
// gets NULL inks and an empty palette, which records that it was read.
func InkColumns(picture []byte) (pgtype.Text, pgtype.Text, []string) {
	if len(picture) == 0 {
		return pgtype.Text{}, pgtype.Text{}, nil
	}
	inks, ok := ReadInks(picture)
	if !ok {
		return pgtype.Text{}, pgtype.Text{}, []string{}
	}
	return pgtype.Text{String: inks.Dark, Valid: true},
		pgtype.Text{String: inks.Light, Valid: true},
		inks.Palette
}

// clampInks is inks() in the Duoton prototype, kept step for step so the
// stored inks and the browser's clamp agree. The browser runs it again on what
// is stored, and on clamped inks it changes nothing.
func clampInks(dark, light rgb) (rgb, rgb) {
	if dark.luminance() > light.luminance() {
		dark, light = light, dark
	}
	black, white := rgb{0, 0, 0}, rgb{255, 255, 255}
	for n := 0; dark.luminance() > 0.035 && n < 12; n++ {
		dark = dark.mix(black, 0.18)
	}
	for n := 0; light.luminance() < 0.42 && n < 12; n++ {
		light = light.mix(white, 0.16)
	}
	return dark, light
}

// rgb is a colour in 8-bit channels, held as floats so clusters can average.
type rgb [3]float64

func (c rgb) hex() string {
	channel := func(v float64) int { return int(math.Max(0, math.Min(255, math.Round(v)))) }
	return fmt.Sprintf("#%02x%02x%02x", channel(c[0]), channel(c[1]), channel(c[2]))
}

// rounded is the colour as a hex string would hold it, which is what the
// prototype's clamp works on between steps.
func (c rgb) rounded() rgb {
	for i := range c {
		c[i] = math.Max(0, math.Min(255, math.Round(c[i])))
	}
	return c
}

func (c rgb) mix(other rgb, t float64) rgb {
	a := c.rounded()
	return rgb{
		a[0] + (other[0]-a[0])*t,
		a[1] + (other[1]-a[1])*t,
		a[2] + (other[2]-a[2])*t,
	}.rounded()
}

// luminance is WCAG relative luminance, with the 0.03928 knee the prototype
// uses.
func (c rgb) luminance() float64 {
	linear := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	c = c.rounded()
	return 0.2126*linear(c[0]) + 0.7152*linear(c[1]) + 0.0722*linear(c[2])
}

// lightness and saturation are HSL's, as Python's colorsys gives them.
func (c rgb) lightness() float64 {
	high, low := c.extremes()
	return (high + low) / 2
}

func (c rgb) saturation() float64 {
	high, low := c.extremes()
	if high == low {
		return 0
	}
	lightness := (high + low) / 2
	if lightness <= 0.5 {
		return (high - low) / (high + low)
	}
	return (high - low) / (2 - high - low)
}

func (c rgb) extremes() (float64, float64) {
	r, g, b := c[0]/255, c[1]/255, c[2]/255
	return math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
}

// samplePixels shrinks the picture and returns its opaque pixels.
func samplePixels(picture image.Image) []rgb {
	small := image.NewNRGBA(image.Rect(0, 0, inkSample, inkSample))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), picture, picture.Bounds(), draw.Src, nil)
	pixels := make([]rgb, 0, inkSample*inkSample)
	for offset := 0; offset < len(small.Pix); offset += 4 {
		if small.Pix[offset+3] < 128 {
			continue
		}
		pixels = append(pixels, rgb{
			float64(small.Pix[offset]), float64(small.Pix[offset+1]), float64(small.Pix[offset+2]),
		})
	}
	return pixels
}

type cluster struct {
	colour rgb
	share  float64
}

// kmeans reduces the pixels to at most k colours, most common first.
//
// It starts from the pixels at evenly spaced places in lightness order rather
// than at random ones, so the same cover always gives the same inks.
func kmeans(pixels []rgb, k int) []cluster {
	ordered := append([]rgb(nil), pixels...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].luminance() < ordered[j].luminance()
	})
	if k > len(ordered) {
		k = len(ordered)
	}
	centres := make([]rgb, k)
	for i := range centres {
		centres[i] = ordered[(2*i+1)*len(ordered)/(2*k)]
	}

	assigned := make([]int, len(pixels))
	counts := make([]int, k)
	for round := 0; round < inkRounds; round++ {
		changed := round == 0
		for index, pixel := range pixels {
			nearest, distance := 0, math.Inf(1)
			for centre, colour := range centres {
				d := squaredDistance(pixel, colour)
				if d < distance {
					nearest, distance = centre, d
				}
			}
			if assigned[index] != nearest {
				assigned[index], changed = nearest, true
			}
		}
		sums := make([]rgb, k)
		for i := range counts {
			counts[i] = 0
		}
		for index, pixel := range pixels {
			centre := assigned[index]
			counts[centre]++
			for channel := range pixel {
				sums[centre][channel] += pixel[channel]
			}
		}
		for centre := range centres {
			if counts[centre] == 0 {
				continue
			}
			for channel := range centres[centre] {
				centres[centre][channel] = sums[centre][channel] / float64(counts[centre])
			}
		}
		if !changed {
			break
		}
	}

	clusters := make([]cluster, 0, k)
	for centre, count := range counts {
		if count == 0 {
			continue
		}
		clusters = append(clusters, cluster{
			colour: centres[centre].rounded(),
			share:  float64(count) / float64(len(pixels)),
		})
	}
	sort.SliceStable(clusters, func(i, j int) bool { return clusters[i].share > clusters[j].share })
	return clusters
}

func squaredDistance(a, b rgb) float64 {
	dr, dg, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dr*dr + dg*dg + db*db
}

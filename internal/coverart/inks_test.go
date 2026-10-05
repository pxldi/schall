package coverart

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
)

// paintedCover is a 300 by 300 PNG in one ground colour, with a block of a
// second colour over the given fraction of its rows.
func paintedCover(t *testing.T, ground, detail color.RGBA, detailRows float64) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for y := range 300 {
		for x := range 300 {
			fill := ground
			if float64(y) < detailRows*300 {
				fill = detail
			}
			picture.Set(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func hexLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	var c rgb
	for i := range 3 {
		var v int
		for _, digit := range hex[1+2*i : 3+2*i] {
			v *= 16
			switch {
			case digit >= '0' && digit <= '9':
				v += int(digit - '0')
			default:
				v += int(digit-'a') + 10
			}
		}
		c[i] = float64(v)
	}
	return c.luminance()
}

func TestABlackSleeveWithAGoldTitlePrintsInGold(t *testing.T) {
	cover := paintedCover(t, color.RGBA{8, 8, 10, 255}, color.RGBA{212, 165, 40, 255}, 0.1)

	inks, ok := ReadInks(cover)
	if !ok {
		t.Fatal("ReadInks() = false, want the PNG read")
	}
	// The gold #d4a528 sits just under the 0.42 floor, so the clamp lightens it
	// once.
	if inks.Light != "#dbb34a" {
		t.Fatalf("light = %s, want the gold lightened once, #dbb34a", inks.Light)
	}
	if inks.Dark != "#08080a" {
		t.Fatalf("dark = %s, want the black ground #08080a", inks.Dark)
	}
	if !slices.Equal(inks.Palette, []string{"#08080a", "#d4a528"}) {
		t.Fatalf("palette = %v, want ground then gold", inks.Palette)
	}
}

func TestAGreyCoverPrintsInItsOwnLightGrey(t *testing.T) {
	cover := paintedCover(t, color.RGBA{40, 40, 40, 255}, color.RGBA{200, 200, 200, 255}, 0.5)

	inks, ok := ReadInks(cover)
	if !ok {
		t.Fatal("ReadInks() = false")
	}
	// No colour is saturated, so the light cluster carries the page.
	if inks.Light != "#c8c8c8" {
		t.Fatalf("light = %s, want the light cluster #c8c8c8", inks.Light)
	}
}

func TestATinySpeckDoesNotColourThePage(t *testing.T) {
	// 1% of red on a pale blue-grey cover is a speck, below accentShare.
	cover := paintedCover(t, color.RGBA{180, 190, 200, 255}, color.RGBA{230, 20, 20, 255}, 0.01)

	inks, ok := ReadInks(cover)
	if !ok {
		t.Fatal("ReadInks() = false")
	}
	if inks.Light == "#e61414" {
		t.Fatalf("light = %s, want the speck ignored", inks.Light)
	}
}

func TestStoredInksAreAlwaysLegible(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	for range 40 {
		ground := color.RGBA{uint8(random.IntN(256)), uint8(random.IntN(256)), uint8(random.IntN(256)), 255}
		detail := color.RGBA{uint8(random.IntN(256)), uint8(random.IntN(256)), uint8(random.IntN(256)), 255}
		inks, ok := ReadInks(paintedCover(t, ground, detail, random.Float64()))
		if !ok {
			t.Fatal("ReadInks() = false")
		}
		if l := hexLuminance(t, inks.Dark); l > 0.035 {
			t.Fatalf("ground %v detail %v: dark %s has luminance %.3f, want at most 0.035", ground, detail, inks.Dark, l)
		}
		if l := hexLuminance(t, inks.Light); l < 0.42 {
			t.Fatalf("ground %v detail %v: light %s has luminance %.3f, want at least 0.42", ground, detail, inks.Light, l)
		}
	}
}

func TestTheSameCoverAlwaysGivesTheSameInks(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := range 200 {
		for x := range 200 {
			picture.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8((x * y) % 256), 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, nil); err != nil {
		t.Fatal(err)
	}
	first, _ := ReadInks(encoded.Bytes())
	for range 3 {
		again, _ := ReadInks(encoded.Bytes())
		if again.Dark != first.Dark || again.Light != first.Light || !slices.Equal(again.Palette, first.Palette) {
			t.Fatalf("ReadInks() = %+v then %+v, want the same answer", first, again)
		}
	}
}

// The browser clamps again with inks() from the prototype. These pairs are what
// that function returns, so the two clamps agree.
func TestTheClampIsThePrototypes(t *testing.T) {
	cases := []struct{ dark, light, wantDark, wantLight string }{
		{"#3a5f8a", "#c08040", "#20344c", "#d9b38e"},
		{"#f0e0d0", "#202020", "#202020", "#f0e0d0"},
		{"#13204a", "#ffb59e", "#13204a", "#ffb59e"},
		{"#808080", "#777777", "#2c2c2c", "#b3b3b3"},
	}
	parse := func(hex string) rgb {
		var c rgb
		for i := range 3 {
			var v int
			for _, digit := range hex[1+2*i : 3+2*i] {
				v *= 16
				if digit >= '0' && digit <= '9' {
					v += int(digit - '0')
				} else {
					v += int(digit-'a') + 10
				}
			}
			c[i] = float64(v)
		}
		return c
	}
	for _, c := range cases {
		dark, light := clampInks(parse(c.dark), parse(c.light))
		if dark.hex() != c.wantDark || light.hex() != c.wantLight {
			t.Fatalf("clampInks(%s, %s) = %s, %s, want %s, %s",
				c.dark, c.light, dark.hex(), light.hex(), c.wantDark, c.wantLight)
		}
	}
}

func TestAPictureThatDoesNotDecodeIsRecordedAsRead(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="9" height="9"/></svg>`)
	if _, ok := ReadInks(svg); ok {
		t.Fatal("ReadInks(svg) = true, want false")
	}
	dark, light, palette := InkColumns(svg)
	if dark.Valid || light.Valid || palette == nil || len(palette) != 0 {
		t.Fatalf("InkColumns(svg) = %v %v %v, want NULL inks and an empty palette", dark, light, palette)
	}
	dark, light, palette = InkColumns(nil)
	if dark.Valid || light.Valid || palette != nil {
		t.Fatalf("InkColumns(nil) = %v %v %v, want NULL everywhere", dark, light, palette)
	}
}

func TestASweepReadsTheInksOfCoversCachedBeforeInks(t *testing.T) {
	cover := paintedCover(t, color.RGBA{20, 30, 60, 255}, color.RGBA{240, 120, 90, 255}, 0.4)
	fetched := pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true}
	store := &fakeCoverStore{withoutInks: []db.ReleaseCoversWithoutInksRow{
		{AlbumID: uuid.New(), Image: cover, FetchedAt: fetched},
		{AlbumID: uuid.New(), Image: []byte("<svg/>"), FetchedAt: fetched},
	}}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		t.Fatal("reading inks asked an archive")
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedInks) != 2 {
		t.Fatalf("saved inks for %d covers, want 2", len(store.savedInks))
	}
	read, unreadable := store.savedInks[0], store.savedInks[1]
	if !read.InkDark.Valid || !read.InkLight.Valid || len(read.Palette) == 0 || read.FetchedAt != fetched {
		t.Fatalf("saved %+v, want inks for the picture that was read", read)
	}
	if unreadable.InkDark.Valid || unreadable.Palette == nil {
		t.Fatalf("saved %+v, want an empty palette so the SVG is not read again", unreadable)
	}
}

func TestASweepStoresTheInksWithANewCover(t *testing.T) {
	cover := paintedCover(t, color.RGBA{20, 30, 60, 255}, color.RGBA{240, 120, 90, 255}, 0.4)
	store := &fakeCoverStore{waiting: waiting(1)}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write(cover)
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.saved) != 1 || !store.saved[0].InkDark.Valid || store.saved[0].InkLight.String == "" {
		t.Fatalf("saved = %+v, want the cover saved with its inks", store.saved)
	}
}

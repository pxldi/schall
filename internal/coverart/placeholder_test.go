package coverart

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func encodeJPEG(t *testing.T, shade func(x, y int) color.RGBA) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for y := range 96 {
		for x := range 96 {
			picture.Set(x, y, shade(x, y))
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return encoded.Bytes()
}

// The light grey silhouette a provider serves for an artist with no photo.
func TestPlaceholderRecognisesALightGreySilhouette(t *testing.T) {
	avatar := encodeJPEG(t, func(x, y int) color.RGBA {
		if (x-48)*(x-48)+(y-36)*(y-36) < 300 || (y > 60 && x > 24 && x < 72) {
			return color.RGBA{186, 186, 186, 255}
		}
		return color.RGBA{240, 240, 240, 255}
	})
	if !Placeholder(avatar) {
		t.Error("Placeholder() = false, want true for a light grey silhouette")
	}
}

// A black and white photograph is grey too, and it has shadows.
func TestPlaceholderLeavesABlackAndWhitePhotograph(t *testing.T) {
	photo := encodeJPEG(t, func(x, y int) color.RGBA {
		shade := uint8(40 + (x+y)%200)
		return color.RGBA{shade, shade, shade, 255}
	})
	if Placeholder(photo) {
		t.Error("Placeholder() = true, want false for a photograph with shadows")
	}
}

func TestPlaceholderLeavesALightColourPicture(t *testing.T) {
	pale := encodeJPEG(t, func(int, int) color.RGBA { return color.RGBA{250, 220, 180, 255} })
	if Placeholder(pale) {
		t.Error("Placeholder() = true, want false for a pale colour picture")
	}
}

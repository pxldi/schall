package coverart

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"runtime"
	"testing"
)

// hugePNG is a few hundred bytes that declare a 10000 by 10000 picture. Its
// header is all a decoder reads before it allocates room for every pixel.
func hugePNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(data)))
		out.WriteString(kind)
		out.Write(data)
		_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	}
	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header[0:], 10000)
	binary.BigEndian.PutUint32(header[4:], 10000)
	header[8] = 8 // bit depth
	header[9] = 0 // greyscale
	chunk("IHDR", header)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(make([]byte, 64))
	_ = writer.Close()
	chunk("IDAT", compressed.Bytes())
	chunk("IEND", nil)
	return out.Bytes()
}

// allocated reports how many bytes read allocated.
func allocated(read func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	read()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A picture inside a file or off an archive can declare any size it likes in
// a few bytes. Decoding one that declares far more pixels than any cover has
// would allocate gigabytes and take the process down with it, every time the
// cover sweep came back to the same release.
func TestAPictureDeclaringTooManyPixelsIsRefusedBeforeItIsDecoded(t *testing.T) {
	picture := hugePNG(t)
	const bound = 32 << 20
	for name, read := range map[string]func(){
		"shrink":      func() { _, _ = shrink(picture) },
		"thumbnail":   func() { _, _ = Thumbnail(picture, 100) },
		"inks":        func() { _, _ = ReadInks(picture) },
		"placeholder": func() { _ = Placeholder(picture) },
	} {
		t.Run(name, func(t *testing.T) {
			if used := allocated(read); used > bound {
				t.Fatalf("reading the picture allocated %d bytes, want under %d", used, bound)
			}
		})
	}
}

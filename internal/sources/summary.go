package sources

import (
	"sort"
	"strings"
)

// AudioExtensions are the file types Schall treats as music when grouping a
// remote folder into a candidate.
var AudioExtensions = map[string]bool{
	"mp3": true, "flac": true, "ogg": true, "opus": true, "m4a": true,
	"mp4": true, "wav": true, "aiff": true, "aif": true, "ape": true,
	"wv": true, "alac": true, "dsf": true, "dff": true, "wma": true,
}

// NormalizeExtension reduces a remote filename or extension to a bare, lower
// case extension such as "flac".
func NormalizeExtension(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.LastIndex(value, "."); index >= 0 {
		value = value[index+1:]
	}
	return value
}

// Summarize derives the aggregate fields a candidate is ranked and displayed
// on: dominant format, average bitrate, and total size. The dominant format is
// the extension covering the most files, with a stable alphabetical tie-break.
func (candidate *Candidate) Summarize() {
	counts := make(map[string]int, 2)
	bitRateTotal, bitRateCount, sizeTotal := 0, 0, int64(0)
	for index, file := range candidate.Files {
		extension := NormalizeExtension(file.Extension)
		if extension == "" {
			extension = NormalizeExtension(file.Name)
		}
		candidate.Files[index].Extension = extension
		counts[extension]++
		sizeTotal += file.SizeBytes
		if file.BitRate > 0 {
			bitRateTotal += file.BitRate
			bitRateCount++
		}
	}

	formats := make([]string, 0, len(counts))
	for extension := range counts {
		formats = append(formats, extension)
	}
	sort.Slice(formats, func(left, right int) bool {
		if counts[formats[left]] != counts[formats[right]] {
			return counts[formats[left]] > counts[formats[right]]
		}
		return formats[left] < formats[right]
	})

	candidate.Format = ""
	if len(formats) > 0 {
		candidate.Format = formats[0]
	}
	candidate.TotalSizeBytes = sizeTotal
	// Peers report a bitrate for lossless files too, but it describes nothing
	// about the encoding and invites a comparison against a 320 kbps MP3 that
	// is not meaningful. Ranking never reads it for lossless formats either.
	candidate.AverageBitRate = 0
	if bitRateCount > 0 && !losslessExtensions[candidate.Format] {
		candidate.AverageBitRate = bitRateTotal / bitRateCount
	}
}

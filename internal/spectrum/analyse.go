// Package spectrum measures what the audio in a file is, as opposed to what the
// name on it claims.
//
// A file's extension is a word somebody typed. A FLAC is a lossless container,
// which is usually taken to mean the audio inside it was never squeezed — but a
// file made by decoding a 128 kbps MP3 and encoding the result as FLAC is a
// perfectly honest FLAC of already-damaged audio. Peers share such files, often
// without knowing. Every size, format and bit rate check reads one as the best
// copy on offer.
//
// The damage is visible. A lossy encoder throws away the top of the frequency
// range and the audio stops dead there, so a decoded file that carries nothing
// above 16 kHz was lossy once, whatever container it arrives in. This package
// measures that stopping point.
//
// It is quality and never identity. A cutoff says nothing about which recording
// a file holds — a transcode of the right recording is still the right
// recording — so nothing here may be read by anything that admits or refuses a
// file. It exists so that a person choosing between two copies already proven to
// be one recording can see which of them is the lesser.
package spectrum

import (
	"math"
	"math/bits"
)

// How the audio is cut up for analysis. 4096 samples at 44.1 kHz is about 93 ms,
// which is long enough for a bin to be ten-odd hertz wide and short enough that
// a hundred of them fit in the few seconds that are read.
const (
	frameSize = 4096
	frameHop  = 2048
	maxFrames = 128
)

// bandHz is how wide a band the bins are added up into before anything is read
// off them. A single bin of a single frame is noise; a quarter-kilohertz band
// averaged over a hundred frames is a fact about the music.
const bandHz = 250

// What counts as content rather than as the silence an encoder leaves behind.
//
// A band counts when it is loud enough to be part of the music twice over: not
// more than contentBelowPeakDB quieter than the loudest band in the file, and
// not below contentFloorDBFS in absolute terms. The first is what separates
// music from encoder noise in a loud recording, the second stops a very quiet
// recording's own noise floor being read as content.
const (
	contentBelowPeakDB = 60.0
	contentFloorDBFS   = -85.0
)

// silentDBFS is the level below which the segment that was read is not music at
// all — a gap between tracks, or a lead-in. Nothing is concluded from it: the
// file is recorded as looked at, with no cutoff and no suspicion.
const silentDBFS = -70.0

// Measurement is what one look at a file's audio found.
type Measurement struct {
	// CutoffHz is the top of the highest band that still carries music. Zero
	// means nothing was concluded, which is not the same as a low cutoff.
	CutoffHz int
	// CliffHeightDB is how far the audio falls between the band below the cutoff
	// and the band above it. See Suspicion: only a fall counts as a transcode.
	CliffHeightDB float64
	// Judged reports that the segment held music and a cutoff was found. A false
	// here is a look that concluded nothing, and every field above is empty.
	Judged bool
}

// Analyse finds where the music in these samples stops.
//
// samples are mono, in the range -1 to 1, at sampleRateHz. It reads the average
// spectrum over the whole run rather than any one moment, because a cymbal in
// one bar and none in the next is a fact about the arrangement, not about the
// encoder.
func Analyse(samples []float64, sampleRateHz int) Measurement {
	if sampleRateHz <= 0 || len(samples) < frameSize {
		return Measurement{}
	}
	power := averagePower(samples)
	if power == nil {
		return Measurement{}
	}
	bands := intoBands(power, sampleRateHz)
	if len(bands) < 2 {
		return Measurement{}
	}

	peak := math.Inf(-1)
	for _, level := range bands {
		peak = math.Max(peak, level)
	}
	if peak < silentDBFS {
		return Measurement{}
	}
	floor := math.Max(peak-contentBelowPeakDB, contentFloorDBFS)

	top := -1
	for index := len(bands) - 1; index >= 0; index-- {
		if bands[index] >= floor {
			top = index
			break
		}
	}
	if top < 0 {
		return Measurement{}
	}
	// A top band that is the last band is a file whose music runs to the edge of
	// what its sample rate can hold. That is the ordinary answer for audio that
	// was never squeezed, and it is reported with no fall, because there is no
	// band above it to fall to.
	cliff := 0.0
	if top+1 < len(bands) {
		cliff = bands[top] - bands[top+1]
	}
	return Measurement{
		CutoffHz:      (top + 1) * bandHz,
		CliffHeightDB: cliff,
		Judged:        true,
	}
}

// averagePower returns the mean power of every bin over every frame, in a
// Hann-windowed short-time transform of the samples.
func averagePower(samples []float64) []float64 {
	window := hann(frameSize)
	total := make([]float64, frameSize/2+1)
	frames := 0
	for start := 0; start+frameSize <= len(samples) && frames < maxFrames; start += frameHop {
		real := make([]float64, frameSize)
		imaginary := make([]float64, frameSize)
		for index := range frameSize {
			real[index] = samples[start+index] * window[index]
		}
		transform(real, imaginary)
		for bin := range total {
			total[bin] += real[bin]*real[bin] + imaginary[bin]*imaginary[bin]
		}
		frames++
	}
	if frames == 0 {
		return nil
	}
	for bin := range total {
		total[bin] /= float64(frames)
	}
	return total
}

// intoBands averages the bins into bands of bandHz and reports each band in
// decibels relative to full scale.
func intoBands(power []float64, sampleRateHz int) []float64 {
	binHz := float64(sampleRateHz) / float64(frameSize)
	count := sampleRateHz / 2 / bandHz
	if count < 1 {
		return nil
	}
	sums := make([]float64, count)
	counts := make([]int, count)
	for bin, value := range power {
		band := int(float64(bin) * binHz / bandHz)
		if band >= count {
			continue
		}
		sums[band] += value
		counts[band]++
	}
	levels := make([]float64, 0, count)
	for band := range count {
		if counts[band] == 0 {
			levels = append(levels, math.Inf(-1))
			continue
		}
		levels = append(levels, decibels(sums[band]/float64(counts[band])))
	}
	return levels
}

// decibels turns a mean power into a level relative to full scale. The window
// spreads a full-scale tone over a few bins and halves its amplitude, so the
// reference is corrected for it; nothing here depends on the absolute number
// being exact, only on it being the same for every band and every file.
func decibels(power float64) float64 {
	if power <= 0 {
		return math.Inf(-1)
	}
	amplitude := math.Sqrt(power) / (frameSize / 4)
	if amplitude <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(amplitude)
}

func hann(size int) []float64 {
	window := make([]float64, size)
	for index := range size {
		window[index] = 0.5 * (1 - math.Cos(2*math.Pi*float64(index)/float64(size-1)))
	}
	return window
}

// transform is an in-place radix-2 fast Fourier transform of a power-of-two
// number of samples.
//
// It is written out here rather than taken from a library because it is thirty
// lines and Schall asks before it adds a dependency. The frame size is fixed and
// a power of two, so none of the general cases a library handles arise.
func transform(real, imaginary []float64) {
	size := len(real)
	shift := bits.TrailingZeros(uint(size))

	// Reorder the samples so that the butterflies below read them in place.
	for index := range size {
		mirrored := int(bits.Reverse(uint(index)) >> (bits.UintSize - shift))
		if mirrored > index {
			real[index], real[mirrored] = real[mirrored], real[index]
			imaginary[index], imaginary[mirrored] = imaginary[mirrored], imaginary[index]
		}
	}
	for width := 2; width <= size; width <<= 1 {
		angle := -2 * math.Pi / float64(width)
		for start := 0; start < size; start += width {
			for offset := range width / 2 {
				turn := angle * float64(offset)
				cos, sin := math.Cos(turn), math.Sin(turn)
				low, high := start+offset, start+offset+width/2
				realPart := cos*real[high] - sin*imaginary[high]
				imaginaryPart := sin*real[high] + cos*imaginary[high]
				real[high] = real[low] - realPart
				imaginary[high] = imaginary[low] - imaginaryPart
				real[low] += realPart
				imaginary[low] += imaginaryPart
			}
		}
	}
}

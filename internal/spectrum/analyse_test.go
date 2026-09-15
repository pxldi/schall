package spectrum

import (
	"math"
	"math/rand"
	"testing"
)

// The audio these tests are made of.
//
// Nothing is read from disk. A transcode is audible as one thing and one thing
// only — the music stops at a frequency no lossless encoder would stop it at —
// so the two files worth testing are a tone mix that runs to twenty kilohertz
// and the same mix with everything above fifteen removed. Both are generated
// here, which means the test says what it is testing rather than pointing at a
// file somebody would have to open to find out.
const testSampleRate = 44100

// mix builds a second and a half of audio holding one tone per frequency, all at
// the same level, plus a very quiet noise floor. The floor is what a real file
// has and a synthesised one does not: without it every band above the highest
// tone is mathematically empty, which is a cliff no real encoder ever produces
// and would make the measurement look better than it is.
func mix(frequencies []float64, floor float64) []float64 {
	samples := make([]float64, testSampleRate*3/2)
	random := rand.New(rand.NewSource(1))
	for index := range samples {
		value := random.NormFloat64() * floor
		for _, frequency := range frequencies {
			value += 0.2 * math.Sin(2*math.Pi*frequency*float64(index)/testSampleRate)
		}
		samples[index] = value
	}
	return samples
}

// wideBands is music that runs to the top of what 44.1 kHz can hold, which is
// what audio that was never squeezed looks like.
var wideBands = []float64{200, 1000, 3000, 7000, 12000, 16500, 18000, 20000}

// cutBands is the same music with everything above fifteen kilohertz removed,
// which is what a lossy encoder does and what survives being wrapped in a
// lossless container afterwards.
var cutBands = []float64{200, 1000, 3000, 7000, 12000, 14500}

func TestAnalyseFindsTheTopOfWideAudio(t *testing.T) {
	measurement := Analyse(mix(wideBands, 1e-6), testSampleRate)

	if !measurement.Judged {
		t.Fatal("audio full of tones was not judged at all")
	}
	if measurement.CutoffHz < 19500 {
		t.Fatalf("cutoff = %d Hz, want the 20 kHz tone to be found", measurement.CutoffHz)
	}
}

func TestAnalyseFindsWhereLossyAudioStops(t *testing.T) {
	measurement := Analyse(mix(cutBands, 1e-6), testSampleRate)

	if !measurement.Judged {
		t.Fatal("audio full of tones was not judged at all")
	}
	if measurement.CutoffHz > 15500 {
		t.Fatalf("cutoff = %d Hz, want the audio to stop by 15 kHz", measurement.CutoffHz)
	}
	if measurement.CliffHeightDB < cliffDB {
		t.Fatalf("cliff = %.1f dB, want a stop steep enough to count as one",
			measurement.CliffHeightDB)
	}
}

// Silence concludes nothing. The middle of a track can be a gap, and a file that
// was looked at and said nothing must never come back as a file with no high
// frequencies.
func TestAnalyseConcludesNothingAboutSilence(t *testing.T) {
	if measurement := Analyse(make([]float64, testSampleRate), testSampleRate); measurement.Judged {
		t.Fatalf("silence was judged, and said the music stops at %d Hz", measurement.CutoffHz)
	}
}

func TestAnalyseConcludesNothingWithoutEnoughAudio(t *testing.T) {
	if measurement := Analyse(make([]float64, 100), testSampleRate); measurement.Judged {
		t.Fatal("a hundred samples were judged")
	}
	if measurement := Analyse(mix(wideBands, 1e-6), 0); measurement.Judged {
		t.Fatal("audio with no stated sample rate was judged")
	}
}

// A FLAC holding audio that stops at fifteen kilohertz was lossy once. Nothing
// lossless removes anything, so the removal happened before the container did.
func TestSuspectedFlagsALosslessContainerThatStopsEarly(t *testing.T) {
	measurement := Analyse(mix(cutBands, 1e-6), testSampleRate)

	if !Suspected(measurement, "/music/track.flac", 0) {
		t.Fatalf("a FLAC stopping at %d Hz was not suspected", measurement.CutoffHz)
	}
	if !Suspected(measurement, "/music/TRACK.FLAC", 0) {
		t.Fatal("the extension was read case-sensitively")
	}
}

// The same audio in the container it came from is not suspected of anything. A
// 128 kbps MP3 that stops at fifteen kilohertz is an ordinary 128 kbps MP3.
func TestSuspectedLeavesAnHonestLossyFileAlone(t *testing.T) {
	measurement := Analyse(mix(cutBands, 1e-6), testSampleRate)

	if Suspected(measurement, "/music/track.mp3", 128) {
		t.Fatal("an ordinary 128 kbps MP3 was called a transcode")
	}
}

// A bit rate wide enough to carry the whole range, spent on audio that stops
// well short of it, is the second contradiction: the file is paying for bits it
// has nothing to put in.
func TestSuspectedFlagsAWideBitRateSpentOnNarrowAudio(t *testing.T) {
	measurement := Analyse(mix(cutBands, 1e-6), testSampleRate)

	if !Suspected(measurement, "/music/track.mp3", 320) {
		t.Fatal("a 320 kbps file stopping at 15 kHz was not suspected")
	}
}

// A real FLAC is never accused, whatever else is true of it.
func TestSuspectedLeavesWideAudioAlone(t *testing.T) {
	measurement := Analyse(mix(wideBands, 1e-6), testSampleRate)

	if Suspected(measurement, "/music/track.flac", 0) {
		t.Fatalf("a FLAC running to %d Hz was called a transcode", measurement.CutoffHz)
	}
}

// The guard the whole feature rests on. A recording that fades out at the top —
// an old one, a dark one, one taken off tape — stops without a cliff, and a
// measurement that called that a transcode would be accusing an honest file.
//
// The audio here has no tones above twelve kilohertz and a noise floor that
// slopes away instead of stopping, which is what a gradual roll-off looks like.
func TestSuspectedLeavesADarkRecordingAlone(t *testing.T) {
	samples := taperedNoise()
	measurement := Analyse(samples, testSampleRate)

	if !measurement.Judged {
		t.Fatal("a dark recording was not judged at all")
	}
	if measurement.CutoffHz > transcodeCutoffHz {
		t.Fatalf("the tapered audio reaches %d Hz, so the cliff is not what is being tested",
			measurement.CutoffHz)
	}
	if Suspected(measurement, "/music/track.flac", 0) {
		t.Fatalf("a recording that fades out at %d Hz was called a transcode, cliff %.1f dB",
			measurement.CutoffHz, measurement.CliffHeightDB)
	}
}

// taperedNoise is audio whose level falls away smoothly with frequency rather
// than stopping: four one-pole low-pass filters over white noise, which fades
// out at about a quarter of a decibel per band. That is what an old recording
// does. A lossy encoder falls tens of decibels between one band and the next.
func taperedNoise() []float64 {
	random := rand.New(rand.NewSource(7))
	samples := make([]float64, testSampleRate*3/2)
	// A coefficient that puts the corner around two and a half kilohertz. Four
	// stages of it fall about twenty-four decibels an octave above that, so the
	// music has faded out by fourteen kilohertz without ever stopping.
	const smoothing = 0.30
	stages := make([]float64, 4)
	for index := range samples {
		value := random.NormFloat64() * 0.5
		for stage := range stages {
			stages[stage] += smoothing * (value - stages[stage])
			value = stages[stage]
		}
		samples[index] = value
	}
	return samples
}

// The transform is the one piece of arithmetic here that could be silently
// wrong, so it is checked against a signal whose answer is known: a single tone
// puts all of its energy in one bin.
func TestTransformPutsAToneInItsOwnBin(t *testing.T) {
	const bin = 64
	real := make([]float64, frameSize)
	imaginary := make([]float64, frameSize)
	for index := range real {
		real[index] = math.Cos(2 * math.Pi * bin * float64(index) / frameSize)
	}
	transform(real, imaginary)

	loudest, level := 0, 0.0
	for index := range frameSize / 2 {
		power := real[index]*real[index] + imaginary[index]*imaginary[index]
		if power > level {
			loudest, level = index, power
		}
	}
	if loudest != bin {
		t.Fatalf("the loudest bin is %d, want %d", loudest, bin)
	}
}

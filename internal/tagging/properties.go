package tagging

import (
	"fmt"

	"go.senan.xyz/taglib"
)

// AudioProperties is what the stream in a file actually is, as opposed to what
// the name on it suggests.
//
// It is quality and never identity. Nothing here says what music the file
// holds, and nothing here may ever be read as evidence that two files hold the
// same music: a bitrate agrees with every other file of the same bitrate in the
// world. It exists so that two copies already proven to be one recording can be
// told apart.
type AudioProperties struct {
	// BitRateKbps is kbit/s as the decoder reports it, which for a variable
	// rate is the average over the whole stream.
	BitRateKbps  int
	SampleRateHz int
	Channels     int
	// DurationMS is measured off the stream rather than read out of a tag, and
	// is not what the scanner stores as duration_ms. See migration 00045.
	DurationMS int
}

// Known reports whether the audio was read at all. A sample rate is the one
// value every readable stream has, and verify already treats its absence as the
// file no longer being audio.
func (properties AudioProperties) Known() bool { return properties.SampleRateHz > 0 }

// ReadProperties reads what the audio in this file is.
//
// It opens the file and parses its headers; it does not decode. A file it
// cannot make sense of is not an error worth stopping a scan for — plenty of
// audio Schall is happy to hold says nothing this can read — so the caller is
// expected to record that the question was asked and leave the answer empty.
//
// The error is not how that is told, either: taglib answers for a file that is
// not audio at all with an empty answer and no complaint. Known is what
// separates "nothing to say" from "something to say", whichever way the read
// went.
func ReadProperties(path string) (AudioProperties, error) {
	properties, err := taglib.ReadProperties(path)
	if err != nil {
		return AudioProperties{}, fmt.Errorf("read the audio in %s: %w", path, err)
	}
	return AudioProperties{
		BitRateKbps:  int(properties.Bitrate),
		SampleRateHz: int(properties.SampleRate),
		Channels:     int(properties.Channels),
		DurationMS:   int(properties.Length.Milliseconds()),
	}, nil
}

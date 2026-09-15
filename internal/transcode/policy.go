// Package transcode writes a smaller copy of music that is already proven.
//
// It exists because a lossless file is five to ten times the size of a good
// MP3 of the same song, and a collection kept on a small disc runs out of room
// long before it runs out of music. An operator who would rather have the
// music than the last of the detail can ask for higher formats to be filed as
// MP3 instead of as they arrived.
//
// Nothing here decides what a file is, and nothing here may ever be read as
// evidence. Every identification — AcoustID, the distributor's published
// sample, the tags, the duration — is made on the bytes that arrived, and the
// verdict is recorded against those bytes. A re-encode happens after that
// verdict and only after it, so this package can admit nothing: a file it never
// sees is a file that was never proven.
package transcode

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pxldi/schall/internal/tagging"
)

// TargetMP3 is the one format a copy can be written as. Opus and AAC are the
// obvious next answers and the setting is an enumeration so that adding one is
// a value rather than a second column, but only MP3 is implemented.
const TargetMP3 = "mp3"

// BitrateV0 is LAME's variable-rate setting. It spends bits where the music
// needs them rather than at a fixed rate, and averages around v0NominalKbps.
const BitrateV0 = "V0"

// v0NominalKbps is what V0 is treated as when a bit rate has to be compared to
// a number. It is an average rather than a promise: a quiet passage encodes far
// below it and a dense one far above.
const v0NominalKbps = 245

// Which arrivals are re-encoded.
//
// WhenLossless is the cautious answer: only a format that lost nothing in the
// first place, so nothing already lossy is put through a second encoder.
// WhenAboveTarget also takes a lossy file whose bit rate is higher than the
// target's — cheaper on disc, and a generation of loss the operator asked for.
const (
	WhenLossless    = "lossless"
	WhenAboveTarget = "above_target"
)

// Bitrates are the settings that may be stored, in the order a chooser shows
// them. The database holds the same list; this one is what a request is checked
// against before it reaches the database.
var Bitrates = []string{BitrateV0, "128", "160", "192", "224", "256", "288", "320"}

// Policy is the operator's answer to "shrink my music, and how".
//
// The zero value is the answer an installation that has never been asked gives:
// nothing is re-encoded.
type Policy struct {
	Enabled bool
	Target  string
	Bitrate string
	When    string
}

// Default is the policy a fresh installation runs under: off, and if it were
// turned on it would take lossless files to 320 kbit/s MP3.
func Default() Policy {
	return Policy{Target: TargetMP3, Bitrate: "320", When: WhenLossless}
}

// Validate reports a policy that cannot be stored. The database refuses the
// same values; this says which one was wrong in a sentence a person can read.
func (policy Policy) Validate() error {
	if policy.Target != TargetMP3 {
		return fmt.Errorf("target must be %q", TargetMP3)
	}
	if !contains(Bitrates, policy.Bitrate) {
		return fmt.Errorf("bitrate must be one of %s", strings.Join(Bitrates, ", "))
	}
	if policy.When != WhenLossless && policy.When != WhenAboveTarget {
		return fmt.Errorf("when must be %q or %q", WhenLossless, WhenAboveTarget)
	}
	return nil
}

// NominalKbps is the bit rate this policy is compared by. A fixed rate is
// itself; V0 is the average it works out at.
func (policy Policy) NominalKbps() int {
	if policy.Bitrate == BitrateV0 {
		return v0NominalKbps
	}
	rate, err := strconv.Atoi(policy.Bitrate)
	if err != nil {
		return 0
	}
	return rate
}

// losslessFormats are the extensions that name audio nothing was thrown away
// from. It is a list of names rather than a decode because the name is what an
// import has before it opens anything, and a wrong guess here costs only a
// re-encode that does not happen.
//
// .m4a is deliberately absent. It holds ALAC, which is lossless, and it holds
// AAC, which is not, and the extension does not say which — so a file named
// that way is left alone rather than re-encoded on a guess.
var losslessFormats = []string{
	"flac", "wav", "wave", "alac", "ape", "aiff", "aif", "wv", "tta", "dsf", "dff",
}

// Format is the format label of a file, read off its extension and lowercased.
// It is what the file row shows as "From FLAC", and it is a label rather than a
// fact about the stream.
func Format(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// Lossless reports whether a format label names audio that lost nothing.
func Lossless(format string) bool { return contains(losslessFormats, format) }

// Applies answers whether this one file is re-encoded, given what its name says
// and what its stream turned out to be.
//
// Four things stop it, in this order:
//
//  1. The policy is off, or is not one that can be stored.
//  2. The stream could not be read. A re-encode is checked against the length
//     of what it was made from, so a file that will not say how long it is
//     cannot be checked, and one that cannot be checked is not made.
//  3. The file is already the target format. An MP3 is never re-encoded into an
//     MP3: a 320 kbit/s one is already what the setting asks for, and a
//     192 kbit/s one would only lose a second generation to arrive at a bigger
//     file. Nothing is ever encoded upwards.
//  4. Under WhenLossless the file is not lossless; under WhenAboveTarget it is
//     neither lossless nor above the target rate. An unread bit rate is not
//     "higher than 320" — it is nothing at all, and it was already refused at
//     step two.
func (policy Policy) Applies(name string, properties tagging.AudioProperties) bool {
	if !policy.Enabled || policy.Validate() != nil {
		return false
	}
	if !properties.Known() || properties.DurationMS <= 0 {
		return false
	}
	format := Format(name)
	if format == "" || format == policy.Target {
		return false
	}
	if Lossless(format) {
		return true
	}
	if policy.When != WhenAboveTarget {
		return false
	}
	return properties.BitRateKbps > policy.NominalKbps()
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

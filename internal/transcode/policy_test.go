package transcode

import (
	"testing"

	"github.com/pxldi/schall/internal/tagging"
)

// read is a stream that could be read: every decision that turns on the audio
// needs one, because a file that says nothing about itself is never re-encoded.
func read(bitRate, durationMS int) tagging.AudioProperties {
	return tagging.AudioProperties{
		BitRateKbps: bitRate, SampleRateHz: 44100, Channels: 2, DurationMS: durationMS,
	}
}

func lossless(policy Policy) Policy {
	policy.Target, policy.When = TargetMP3, WhenLossless
	policy.Enabled = true
	if policy.Bitrate == "" {
		policy.Bitrate = "320"
	}
	return policy
}

func TestPolicyOffReEncodesNothing(t *testing.T) {
	policy := Policy{Target: TargetMP3, Bitrate: "320", When: WhenLossless}
	if policy.Applies("Track.flac", read(1000, 200_000)) {
		t.Fatal("a policy nobody turned on re-encoded a file")
	}
}

func TestLosslessPolicyReEncodesAFlac(t *testing.T) {
	if !lossless(Policy{}).Applies("Track.flac", read(1000, 200_000)) {
		t.Fatal("a FLAC was left alone by the lossless policy")
	}
}

func TestLosslessPolicyLeavesALossyFileAlone(t *testing.T) {
	if lossless(Policy{}).Applies("Track.ogg", read(500, 200_000)) {
		t.Fatal("a lossy file was re-encoded by the lossless policy")
	}
}

// An MP3 is never written as another MP3. A 320 kbit/s one is already what the
// setting asks for, and a 192 kbit/s one would lose a second generation to
// arrive at a bigger file.
func TestAnMP3IsNeverReEncoded(t *testing.T) {
	above := lossless(Policy{})
	above.When = WhenAboveTarget
	for _, bitRate := range []int{192, 320, 400} {
		if above.Applies("Track.mp3", read(bitRate, 200_000)) {
			t.Fatalf("a %d kbit/s MP3 was re-encoded", bitRate)
		}
	}
}

func TestAboveTargetReEncodesALossyFileOverTheTargetRate(t *testing.T) {
	policy := lossless(Policy{})
	policy.When = WhenAboveTarget
	if !policy.Applies("Track.ogg", read(500, 200_000)) {
		t.Fatal("a 500 kbit/s file was left alone under above_target")
	}
}

func TestAboveTargetLeavesALossyFileUnderTheTargetRateAlone(t *testing.T) {
	policy := lossless(Policy{})
	policy.When = WhenAboveTarget
	if policy.Applies("Track.ogg", read(192, 200_000)) {
		t.Fatal("a 192 kbit/s file was encoded upwards to 320")
	}
}

// V0 is a variable rate, so the comparison uses the average it works out at
// rather than a number nobody stored.
func TestAboveTargetComparesAVariableRateByItsAverage(t *testing.T) {
	policy := lossless(Policy{Bitrate: BitrateV0})
	policy.When = WhenAboveTarget
	if !policy.Applies("Track.ogg", read(320, 200_000)) {
		t.Fatal("a 320 kbit/s file was not seen as above V0's average")
	}
	if policy.Applies("Track.ogg", read(200, 200_000)) {
		t.Fatal("a 200 kbit/s file was seen as above V0's average")
	}
}

// A stream nothing could read is not evidence of anything. The copy would have
// to be checked against the length of the original, and there is no length.
func TestAFileWhoseAudioCouldNotBeReadIsLeftAlone(t *testing.T) {
	if lossless(Policy{}).Applies("Track.flac", tagging.AudioProperties{}) {
		t.Fatal("a file with no readable stream was re-encoded")
	}
}

// An .m4a is ALAC or it is AAC and the name does not say which, so it is never
// taken for lossless.
func TestAnM4AIsNotTakenForLossless(t *testing.T) {
	if lossless(Policy{}).Applies("Track.m4a", read(256, 200_000)) {
		t.Fatal("an .m4a was re-encoded as though the name proved it lossless")
	}
}

func TestAPolicyThatCannotBeStoredReEncodesNothing(t *testing.T) {
	policy := Policy{Enabled: true, Target: "opus", Bitrate: "320", When: WhenLossless}
	if policy.Applies("Track.flac", read(1000, 200_000)) {
		t.Fatal("a policy the database would refuse was acted on")
	}
}

func TestValidateRefusesABitrateOutsideTheStoredSet(t *testing.T) {
	policy := Policy{Enabled: true, Target: TargetMP3, Bitrate: "999", When: WhenLossless}
	if err := policy.Validate(); err == nil {
		t.Fatal("999 kbit/s was accepted")
	}
}

func TestValidateRefusesAnUnknownWhen(t *testing.T) {
	policy := Policy{Enabled: true, Target: TargetMP3, Bitrate: "320", When: "sometimes"}
	if err := policy.Validate(); err == nil {
		t.Fatal(`"sometimes" was accepted as a rule for which files are re-encoded`)
	}
}

func TestValidateAcceptsTheDefault(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("the default policy is not storable: %v", err)
	}
}

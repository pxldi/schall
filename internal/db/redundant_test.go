package db

import (
	"testing"

	"github.com/google/uuid"
)

// copy describes one file to the judgement the way the query would.
func copyOf(path string, bitRate, sampleRate, channels, measured int) HeldTwiceCopy {
	return HeldTwiceCopy{
		Path:         path,
		BitRateKbps:  pointer(int32(bitRate)),
		SampleRateHz: pointer(int32(sampleRate)),
		Channels:     pointer(int32(channels)),
		measured:     measured,
		mapped:       true,
	}
}

func pointer(value int32) *int32 { return &value }

// What the judgement is for: two copies it can put in an order, and one file
// offered up for removal at the end of it.
func TestJudgeNamesTheBetterCopy(t *testing.T) {
	for name, testCase := range map[string]struct {
		copies []HeldTwiceCopy
		keeper string
		want   string
	}{
		"lossless over lossy": {
			copies: []HeldTwiceCopy{
				copyOf("/music/single.mp3", 320, 44100, 2, 214000),
				copyOf("/music/album.flac", 950, 44100, 2, 214000),
			},
			keeper: "/music/album.flac",
			want:   "Keep the FLAC: it is lossless and the MP3 is not.",
		},
		"the higher bitrate of one codec": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.mp3", 192, 44100, 2, 214000),
				copyOf("/music/b.mp3", 320, 44100, 2, 214000),
			},
			keeper: "/music/b.mp3",
			want:   "Keep the 320 kbps copy: the other is 192 kbps.",
		},
		"a length inside the tolerance is still the same music": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.mp3", 192, 44100, 2, 214000),
				copyOf("/music/b.mp3", 320, 44100, 2, 217000),
			},
			keeper: "/music/b.mp3",
			want:   "Keep the 320 kbps copy: the other is 192 kbps.",
		},
		"the higher sample rate of two lossless containers": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.flac", 950, 44100, 2, 214000),
				copyOf("/music/b.wav", 2304, 96000, 2, 214000),
			},
			keeper: "/music/b.wav",
			want:   "Keep the 96 kHz copy: the other is 44.1 kHz.",
		},
		"two m4a files are one codec whichever codec it is": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.m4a", 256, 44100, 2, 214000),
				copyOf("/music/b.m4a", 900, 44100, 2, 214000),
			},
			keeper: "/music/b.m4a",
			want:   "Keep the 900 kbps copy: the other is 256 kbps.",
		},
		"the same audio, one of it matched": {
			copies: func() []HeldTwiceCopy {
				identified := copyOf("/music/a.flac", 950, 44100, 2, 214000)
				identified.mapped = false
				return []HeldTwiceCopy{identified, copyOf("/music/b.flac", 950, 44100, 2, 214000)}
			}(),
			keeper: "/music/b.flac",
			want: "Both copies are the same audio. Keep the one matched to a " +
				"catalogue track: the other is only identified as this recording.",
		},
		"three copies, one better than both": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.mp3", 192, 44100, 2, 214000),
				copyOf("/music/b.mp3", 256, 44100, 2, 214000),
				copyOf("/music/c.flac", 950, 44100, 2, 214000),
			},
			keeper: "/music/c.flac",
			want:   "Keep the FLAC: it is lossless and the MP3 is not.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			verdict, decided := judge(testCase.copies)
			if !decided {
				t.Fatalf("decided = false, want a keeper; verdict %q", verdict)
			}
			if verdict != testCase.want {
				t.Fatalf("verdict = %q, want %q", verdict, testCase.want)
			}
			for _, copied := range testCase.copies {
				switch {
				case copied.Path == testCase.keeper && !copied.Keeper:
					t.Fatalf("%s is not the keeper", copied.Path)
				case copied.Path == testCase.keeper && copied.Redundant:
					t.Fatalf("%s is the keeper and also redundant", copied.Path)
				case copied.Path != testCase.keeper && !copied.Redundant:
					t.Fatalf("%s was not offered up", copied.Path)
				}
			}
		})
	}
}

// The half that matters. Every case here is one Schall could answer by guessing
// and does not, and each one costs a file somebody wanted if it guesses wrong.
func TestJudgeRefusesWhatNothingSeparates(t *testing.T) {
	for name, testCase := range map[string]struct {
		copies []HeldTwiceCopy
		want   string
	}{
		"the audio of one has never been read": {
			copies: func() []HeldTwiceCopy {
				unread := copyOf("/music/a.mp3", 0, 0, 0, 0)
				unread.SampleRateHz = nil
				return []HeldTwiceCopy{unread, copyOf("/music/b.flac", 950, 44100, 2, 214000)}
			}(),
			want: "Nothing has read the audio of one of these copies yet. " +
				"Rescan the library and this answers itself.",
		},
		"different lengths are different files": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.mp3", 320, 44100, 2, 30000),
				copyOf("/music/b.mp3", 192, 44100, 2, 214000),
			},
			want: "These copies are not the same length. One of them is not " +
				"simply a lesser version of the other.",
		},
		"a bitrate across two codecs": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.mp3", 320, 44100, 2, 214000),
				copyOf("/music/b.opus", 128, 48000, 2, 214000),
			},
			want: "One is MP3 and the other OPUS. A bitrate from one codec " +
				"says nothing about a bitrate from another.",
		},
		"a container that will not say what it holds": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.m4a", 256, 44100, 2, 214000),
				copyOf("/music/b.mp3", 320, 44100, 2, 214000),
			},
			want: "A M4A holds either lossless or lossy audio and nothing " +
				"stored says which, so it cannot be weighed against a MP3.",
		},
		"better in one respect and worse in another": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.mp3", 320, 44100, 2, 214000),
				copyOf("/music/b.mp3", 256, 48000, 2, 214000),
			},
			want: "One copy is better in one respect and worse in another, so " +
				"neither replaces the other.",
		},
		"nothing at all to choose between them": {
			copies: []HeldTwiceCopy{
				copyOf("/music/a.flac", 950, 44100, 2, 214000),
				copyOf("/music/b.flac", 950, 44100, 2, 214000),
			},
			want: "These copies are the same audio and the library holds them " +
				"the same way. Either one will do; Schall will not pick for you.",
		},
		"the better copy is the worse fit": {
			copies: func() []HeldTwiceCopy {
				better := copyOf("/music/a.flac", 950, 44100, 2, 214000)
				better.mapped = false
				return []HeldTwiceCopy{better, copyOf("/music/b.mp3", 320, 44100, 2, 214000)}
			}(),
			want: "The better copy is the one nothing has matched to a " +
				"catalogue track. Schall will not trade a matched file for an " +
				"unmatched one.",
		},
		"a decision somebody made": {
			copies: func() []HeldTwiceCopy {
				chosen := copyOf("/music/a.mp3", 128, 44100, 2, 214000)
				chosen.byHand = true
				return []HeldTwiceCopy{chosen, copyOf("/music/b.flac", 950, 44100, 2, 214000)}
			}(),
			want: "One of these copies was matched by hand. Schall does not " +
				"weigh up a decision somebody already made.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			verdict, decided := judge(testCase.copies)
			if decided {
				t.Fatalf("decided = true, want a refusal; verdict %q", verdict)
			}
			if verdict != testCase.want {
				t.Fatalf("verdict = %q, want %q", verdict, testCase.want)
			}
			for _, copied := range testCase.copies {
				if copied.Keeper || copied.Redundant {
					t.Fatalf("%s was marked though nothing was decided", copied.Path)
				}
			}
		})
	}
}

// Three copies where the best two are indistinguishable: deleting either of a
// tie is a choice nobody made, so the whole group waits.
func TestJudgeRefusesATieAtTheTop(t *testing.T) {
	copies := []HeldTwiceCopy{
		copyOf("/music/a.flac", 950, 44100, 2, 214000),
		copyOf("/music/b.flac", 950, 44100, 2, 214000),
		copyOf("/music/c.mp3", 192, 44100, 2, 214000),
	}
	verdict, decided := judge(copies)
	if decided {
		t.Fatalf("decided = true, want a refusal; verdict %q", verdict)
	}
	for _, copied := range copies {
		if copied.Redundant {
			t.Fatalf("%s was offered up though the two best copies tie", copied.Path)
		}
	}
}

// A lone copy is not a duplicate, and there is nothing to judge.
func TestJudgeSaysNothingAboutOneFile(t *testing.T) {
	copies := []HeldTwiceCopy{copyOf("/music/a.flac", 950, 44100, 2, 214000)}
	if verdict, decided := judge(copies); decided || verdict != "" {
		t.Fatalf("judge = %q, %v; want no verdict", verdict, decided)
	}
}

// identified is one copy the way the query hands it over, with an identifier so
// the chooser's answer can be told from the other copies'.
func identified(path string, bitRate, sampleRate, channels, measured int, size int64) HeldTwiceCopy {
	copied := copyOf(path, bitRate, sampleRate, channels, measured)
	copied.ID = uuid.New()
	copied.SizeBytes = size
	return copied
}

// The whole of what a person means by "they are all fine": two files that are
// the same audio in the same container. One is kept and the answer is the same
// every time it is asked.
func TestAnyWillDoPicksOneOfTwoIdenticalCopies(t *testing.T) {
	first := identified("/music/a/one.mp3", 320, 44100, 2, 200_000, 5_000_000)
	second := identified("/music/b/one.mp3", 320, 44100, 2, 200_000, 4_000_000)

	keeper, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{first, second})
	if !chosen {
		t.Fatal("nothing was chosen from two identical copies")
	}
	// The larger file, then the earlier path — a rule, not a judgement, so that
	// pressing twice keeps the same file.
	if keeper != first.ID {
		t.Errorf("kept %v, wanted the larger copy %v", keeper, first.ID)
	}
	again, _ := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{second, first})
	if again != keeper {
		t.Errorf("a second ask kept %v and the first kept %v", again, keeper)
	}
}

// Where one copy is provably better, that copy is kept — the instruction does
// not override the judgement, it only answers where the judgement had nothing
// to say.
func TestAnyWillDoStillKeepsTheBetterCopyWhereThereIsOne(t *testing.T) {
	lossless := identified("/music/a/one.flac", 900, 44100, 2, 200_000, 30_000_000)
	lossy := identified("/music/b/one.mp3", 320, 44100, 2, 200_000, 5_000_000)

	keeper, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{lossy, lossless})
	if !chosen || keeper != lossless.ID {
		t.Fatalf("kept %v, wanted the lossless copy %v", keeper, lossless.ID)
	}
}

// The refusals are the point. "They are all fine" is something a person can say
// about copies; none of these is a group Schall can show to be copies, so the
// instruction does not reach them and nothing is deleted.
func TestAnyWillDoRefusesWhereTheCopiesAreNotShownToBeTheSameAudio(t *testing.T) {
	sameLength := func() (HeldTwiceCopy, HeldTwiceCopy) {
		return identified("/music/a/one.mp3", 320, 44100, 2, 200_000, 5_000_000),
			identified("/music/b/one.mp3", 320, 44100, 2, 200_000, 4_000_000)
	}

	t.Run("lengths further apart than matching allows", func(t *testing.T) {
		first, second := sameLength()
		second.measured = 200_000 + durationSlack + 1
		if _, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{first, second}); chosen {
			t.Error("chose between two files of different lengths")
		}
	})

	t.Run("nothing has decoded one of them", func(t *testing.T) {
		first, second := sameLength()
		second.SampleRateHz = nil
		if _, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{first, second}); chosen {
			t.Error("chose a copy nothing has read")
		}
	})

	t.Run("two lossy codecs that cannot be compared", func(t *testing.T) {
		first, second := sameLength()
		second.Path = "/music/b/one.opus"
		if _, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{first, second}); chosen {
			t.Error("chose between an MP3 and an Opus file")
		}
	})

	t.Run("somebody matched one by hand", func(t *testing.T) {
		first, second := sameLength()
		second.byHand = true
		if _, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{first, second}); chosen {
			t.Error("weighed up a decision somebody already made")
		}
	})

	// A group is interchangeable or it is not. Two copies tying while a third
	// cannot be compared to either is not a group to pick from.
	t.Run("one pair ties and another cannot be compared", func(t *testing.T) {
		first, second := sameLength()
		third := identified("/music/c/one.mp3", 320, 44100, 2, 400_000, 6_000_000)
		if _, chosen := ChooseKeeperWhenAnyWillDo([]HeldTwiceCopy{first, second, third}); chosen {
			t.Error("chose from a group that is not all one piece of audio")
		}
	})
}

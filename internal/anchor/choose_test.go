package anchor

import "testing"

// The rule these tests pin is ADR 0029 §2. It is the whole of what
// keeps a text search honest: an upload is chosen by length and by name, a
// matching Topic upload wins, and views choose within one kind. A ranking is
// not evidence and a view count breaks a tie between uploads that already agree
// — it never makes one agree.
//
// The want throughout is "Watchfire" by Vela Nine, five minutes and five
// seconds. The sibling case is the one that matters most: a live version, a
// slowed edit and an extended mix all carry the same name.
const (
	wantArtist     = "Vela Nine"
	wantTitle      = "Watchfire"
	wantDurationMS = 305000
)

func TestTheChosenUploadIsTheMostViewedThatFitsBothGates(t *testing.T) {
	tests := []struct {
		name    string
		uploads []Upload
		chosen  string
	}{
		{
			name: "a sibling version running longer is not chosen",
			uploads: []Upload{
				{ID: "extended", Title: "Vela Nine - Watchfire", Seconds: 402, Views: 9_000_000},
				{ID: "album", Title: "Vela Nine - Watchfire", Seconds: 305, Views: 12_000},
			},
			chosen: "album",
		},
		{
			name: "the most viewed of the uploads that fit wins",
			uploads: []Upload{
				{ID: "reupload", Title: "Vela Nine - Watchfire", Seconds: 304, Views: 4_100},
				{ID: "popular", Title: "Vela Nine - Watchfire", Seconds: 306, Views: 2_400_000},
				{ID: "quiet", Title: "Vela Nine - Watchfire", Seconds: 305, Views: 60},
			},
			chosen: "popular",
		},
		{
			name: "an uploader's label comes off the title",
			uploads: []Upload{
				{
					ID: "official", Title: "Vela Nine - Watchfire (Official Video) [4K]",
					Seconds: 305, Views: 800_000,
				},
			},
			chosen: "official",
		},
		{
			name: "a slowed edit is a different recording and is not chosen",
			uploads: []Upload{
				{ID: "slowed", Title: "Vela Nine - Watchfire (slowed)", Seconds: 305, Views: 5_000_000},
			},
			chosen: "",
		},
		{
			name: "so is a remix, however popular",
			uploads: []Upload{
				{
					ID: "remix", Title: "Vela Nine - Watchfire (Kessler Remix) [Official Audio]",
					Seconds: 303, Views: 30_000_000,
				},
			},
			chosen: "",
		},
		{
			name: "an upload the title credits to somebody else is not chosen",
			uploads: []Upload{
				{ID: "cover", Title: "Halden Rowe - Watchfire", Seconds: 305, Views: 700_000},
			},
			chosen: "",
		},
		{
			name: "a guest on the credit is still the artist",
			uploads: []Upload{
				{ID: "guest", Title: "Vela Nine feat. Halden Rowe - Watchfire", Seconds: 305, Views: 90},
			},
			chosen: "guest",
		},
		{
			name: "a title that names nobody is read on its title alone",
			uploads: []Upload{
				{ID: "bare", Title: "Watchfire (Official Audio)", Seconds: 305, Views: 40},
			},
			chosen: "bare",
		},
		{
			name: "uploads that all fit and all report no views keep search order",
			uploads: []Upload{
				{ID: "first", Title: "Vela Nine - Watchfire", Seconds: 305},
				{ID: "second", Title: "Vela Nine - Watchfire", Seconds: 305},
			},
			chosen: "first",
		},
		{
			name: "an upload with no length cannot be keyed",
			uploads: []Upload{
				{ID: "unlisted", Title: "Vela Nine - Watchfire", Views: 3_000_000},
			},
			chosen: "",
		},
		{
			name:    "nothing at all is silence",
			uploads: nil,
			chosen:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chosen, found, _ := chooseUpload(test.uploads, wantArtist, wantTitle, wantDurationMS)
			if test.chosen == "" {
				if found {
					t.Fatalf("chose %q, wanted no candidate", chosen.ID)
				}
				return
			}
			if !found {
				t.Fatalf("chose nothing, wanted %q", test.chosen)
			}
			if chosen.ID != test.chosen {
				t.Errorf("chose %q, wanted %q", chosen.ID, test.chosen)
			}
		})
	}
}

func TestTopicUploadWinsAndRequiresTheWantArtist(t *testing.T) {

	chosen, found, topic := chooseUpload([]Upload{
		{ID: "popular", Title: "Vela Nine - Watchfire", Channel: "Uploader", Seconds: 305, Views: 50_000_000},
		{ID: "topic", Title: "Vela Nine - Watchfire", Channel: " Vela Nine - tOpIc ", Seconds: 305, Views: 100},
	}, wantArtist, wantTitle, wantDurationMS)
	if !found || chosen.ID != "topic" || !topic {
		t.Fatalf("chose %q, found %v, topic %v; wanted the matching Topic upload", chosen.ID, found, topic)
	}

	chosen, found, topic = chooseUpload([]Upload{
		{ID: "other-topic", Title: "Vela Nine - Watchfire", Channel: "Halden Rowe - Topic", Seconds: 305, Views: 50_000_000},
		{ID: "right-topic", Title: "Vela Nine - Watchfire", Channel: "Vela Nine - Topic", Seconds: 305, Views: 100},
	}, wantArtist, wantTitle, wantDurationMS)
	if !found || chosen.ID != "right-topic" || !topic {
		t.Fatalf("chose %q, found %v, topic %v; wanted the want artist's Topic upload", chosen.ID, found, topic)
	}
}
func TestUploadTitlesReadFeaturingCreditsAndDashes(t *testing.T) {
	tests := []struct {
		name, artist, title string
		upload              Upload
		want                string
	}{
		{"featuring credit after the title", "Travis Scott", "SICKO MODE", Upload{ID: "sicko", Title: "Travis Scott - SICKO MODE (Official Video) ft. Drake", Seconds: 323}, "sicko"},
		{"bracketed featuring credit", "Playboi Carti", "Damn Shame", Upload{ID: "damn-shame", Title: "Playboi Carti - Damn Shame (feat. Johnny Cinco)", Seconds: 243}, "damn-shame"},
		{"wrong featured artist", "Travis Scott", "SICKO MODE", Upload{ID: "wrong-guest", Title: "Travis Scott - SICKO MODE ft. Somebody Else", Seconds: 323}, "wrong-guest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chosen, found, _ := chooseUpload([]Upload{test.upload}, test.artist, test.title, test.upload.Seconds*1000)
			if test.want == "" {
				if found {
					t.Fatalf("chose %q, wanted no candidate", chosen.ID)
				}
				return
			}
			if !found || chosen.ID != test.want {
				t.Fatalf("chose %q, found %v, wanted %q", chosen.ID, found, test.want)
			}
		})
	}
}

// A want with no length has no second gate, so there is no choice to make. It is
// silence rather than a title match on its own: half the rule is not the rule.
func TestAWantWithNoLengthChoosesNothing(t *testing.T) {
	uploads := []Upload{{ID: "exact", Title: "Vela Nine - Watchfire", Seconds: 305, Views: 9_000}}
	if _, found, _ := chooseUpload(uploads, wantArtist, wantTitle, 0); found {
		t.Fatal("a want with no length was anchored to an upload")
	}
}

func TestUploaderLabelsComeOffAndVersionWordsStay(t *testing.T) {
	tests := []struct {
		raw    string
		artist string
		title  string
	}{
		{"Vela Nine - Watchfire (Official Video)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire [OFFICIAL AUDIO] (HD)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire (Official Music Video)", "Vela Nine", "Watchfire (Official Music Video)"},
		{"Vela Nine - Watchfire (Lyrics)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire (Unreleased) (CDQ)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire (Clean)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire (Explicit)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire (sped up)", "Vela Nine", "Watchfire (sped up)"},
		{"Vela Nine - Watchfire [Instrumental]", "Vela Nine", "Watchfire [Instrumental]"},
		// A producer credit names who made the track, so it comes off with the
		// uploader's own labels (ADR 0031). It has to open the group:
		// a group that merely ends with the word is kept.
		{"Vela Nine - Watchfire [Prod. Halden Rowe]", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire (Produced by Halden Rowe)", "Vela Nine", "Watchfire"},
		{"Vela Nine - Watchfire [Halden Rowe Prod.]", "Vela Nine", "Watchfire [Halden Rowe Prod.]"},
		{"Watchfire", "", "Watchfire"},
		{"Vela Nine – Watchfire (Official Video)", "Vela Nine", "Watchfire"},
		{"Travis Scott, Jason Eric- Looking Out The Window (Buddy Rich)", "Travis Scott, Jason Eric", "Looking Out The Window (Buddy Rich)"},
		{"Playboi Carti - Damn Shame [ft. Johnny Cinco]", "Playboi Carti", "Damn Shame"},
		{"Self-Titled", "", "Self-Titled"},
	}

	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			artist, title := readUpload(test.raw)
			if artist != test.artist || title != test.title {
				t.Errorf("read %q by %q, wanted %q by %q", title, artist, test.title, test.artist)
			}
		})
	}
}

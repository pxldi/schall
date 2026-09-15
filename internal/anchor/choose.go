package anchor

import (
	"regexp"
	"strings"

	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/tagmatch"
)

// How many results the choice is made from. Ten is what docs/decisions/0029
// fixes, and it is a cap on how far down a ranking Schall will look rather than
// a target: a want whose upload is not in the first ten has no upload here.
const uploadResults = 10

// chooseUpload picks the upload a want is anchored to.
//
// The rule is docs/decisions/0029 §2 and it chooses rather than scores: an
// upload is a candidate only when its length is within the tag grader's
// tolerance of the want's and its title agrees with the want's under that same
// grader, and a matching Topic candidate wins over any other candidate. Views
// choose among candidates of the same kind. Ties keep search order. No candidate
// is silence, and so is a want with no length, because
// length is half of what makes the choice a choice.
//
// artist is the want's whole credit, which is what the credit rule is written
// against; the primary credit is what the query is built from, and the two are
// deliberately different.
func chooseUpload(uploads []Upload, artist, title string, durationMS int) (Upload, bool, bool) {
	if durationMS <= 0 {
		return Upload{}, false, false
	}
	var chosen Upload
	found, chosenTopic := false, false
	for _, upload := range uploads {
		if !uploadFits(upload, artist, title, durationMS) {
			continue
		}
		topic := topicUpload(upload, artist)
		if !found || topic && !chosenTopic || topic == chosenTopic && upload.Views > chosen.Views {
			chosen, found, chosenTopic = upload, true, topic
		}
	}
	return chosen, found, chosenTopic
}

// topicUpload reports whether YouTube's generated channel belongs to the want's
// artist. The suffix alone is not enough: another artist's Topic channel may
// carry a cover with the same title and length.
func topicUpload(upload Upload, artist string) bool {
	channel := strings.TrimSpace(upload.Channel)
	const suffix = " - topic"
	if len(channel) < len(suffix) || !strings.HasSuffix(strings.ToLower(channel), suffix) {
		return false
	}
	channelArtist := strings.TrimSpace(channel[:len(channel)-len(suffix)])
	return tagmatch.Credit(channelArtist, artist) == tagmatch.Agrees
}

// uploadFits reports one upload passing both gates. Neither gate is a score and
// neither may be traded against the other: a sibling version of the song carries
// the same name, and the length is what tells it apart.
func uploadFits(upload Upload, artist, title string, durationMS int) bool {
	if strings.TrimSpace(upload.ID) == "" || upload.Seconds <= 0 {
		return false
	}
	if abs(upload.Seconds*1000-durationMS) > identity.DurationToleranceMS {
		return false
	}
	uploadArtist, uploadTitle := readUpload(upload.Title)
	if tagmatch.Title(uploadTitle, title) != tagmatch.Agrees {
		return false
	}
	// The channel is never read as the artist. A channel is a label, an
	// aggregator or a person, and reading it as a credit would let any upload
	// agree with any want. Where the title names nobody, the credit is silent,
	// and silence does not disqualify a title that agrees.
	if uploadArtist == "" {
		return true
	}
	return tagmatch.Credit(uploadArtist, artist) != tagmatch.Differs
}

// The separators an uploader puts between the artist and the track.
var uploadSeparators = []string{" - ", " – ", " — "}
var uploadSeparatorsWithoutLeadingSpace = []string{"- ", "– ", "— "}

var featuringCredit = regexp.MustCompile(`(?i)\s+(ft\.?|feat\.?|featuring)\s+[^()[\]]+$`)
var bracketedFeaturingCredit = regexp.MustCompile(`(?i)\s*[\(\[]\s*(ft\.?|feat\.?|featuring)\s+[^()[\]]+[\)\]]\s*$`)

// readUpload splits an upload title into the artist it names and the recording
// it names, with the uploader's own labels dropped first.
//
// A title with no separator names no artist. That is silence rather than a
// guess: "Ivy" uploaded by a channel called anything at all says nothing about
// who the track is by, and inventing a credit for it would be the guess this
// repository does not make.
func readUpload(raw string) (artist, title string) {
	cleaned := tagmatch.WithoutBracketedLabels(raw)
	for _, separators := range [][]string{uploadSeparators, uploadSeparatorsWithoutLeadingSpace} {
		for _, separator := range separators {
			left, right, found := strings.Cut(cleaned, separator)
			if !found {
				continue
			}
			left, right = strings.TrimSpace(left), stripFeaturingCredit(strings.TrimSpace(right))
			if left != "" && right != "" {
				return left, right
			}
		}
	}
	return "", stripFeaturingCredit(cleaned)
}

func stripFeaturingCredit(title string) string {
	title = bracketedFeaturingCredit.ReplaceAllString(title, "")
	return strings.TrimSpace(featuringCredit.ReplaceAllString(title, ""))
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

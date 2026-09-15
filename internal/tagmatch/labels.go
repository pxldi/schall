package tagmatch

import (
	"strings"
	"unicode"
)

// bracketedLabels are the words that describe the copy of a track rather than
// the recording in it. A bracketed group made only of these is whoever wrote
// the title talking about their own upload or their own file, so it is dropped
// before the title is compared.
//
// What is deliberately absent matters more than what is here. "clean",
// "explicit", "remix", "edit", "demo", "snippet", "slowed", "sped up" and
// "instrumental" all name a different recording of the same song, and dropping
// any of them would let the wrong version answer for the want.
var bracketedLabels = map[string]bool{
	"official": true, "video": true, "audio": true, "lyric": true, "lyrics": true,
	"visualizer": true, "visualiser": true, "hq": true, "hd": true, "4k": true,
	"cdq": true, "unreleased": true, "leak": true, "leaked": true, "full": true,
	"mv": true, "prod": true, "explicit": true, "clean": true,
}

// producerLabels open a bracketed group that names who made the track. The
// group is dropped whole, whatever follows the label: MusicBrainz holds
// "Resin [Prod. GeeKey]" and "Resin" as two rows of one recording, and a
// producer credit is a fact about who was in the room rather than about which
// recording this is.
//
// Only the first word is read. A group that merely mentions a producer
// somewhere inside it is kept, which is the safe direction.
var producerLabels = map[string]bool{"prod": true, "produced": true}

// The brackets a title carries a label in.
var bracketPairs = map[rune]rune{'(': ')', '[': ']', '{': '}'}

// WithoutBracketedLabels drops the bracketed groups that describe the copy or
// name its producer.
//
// A group with one word outside the list is kept whole, and that is the safe
// direction: "(Official Video)" goes and "(Official Video Slowed)" stays, so
// the title of a slowed upload keeps the word that makes it disagree with the
// want.
func WithoutBracketedLabels(title string) string {
	runes := []rune(title)
	var kept strings.Builder
	for index := 0; index < len(runes); index++ {
		closer, opens := bracketPairs[runes[index]]
		if !opens {
			kept.WriteRune(runes[index])
			continue
		}
		end := -1
		for scan := index + 1; scan < len(runes); scan++ {
			if runes[scan] == closer {
				end = scan
				break
			}
		}
		if end < 0 {
			kept.WriteRune(runes[index])
			continue
		}
		if !strippable(string(runes[index+1 : end])) {
			kept.WriteString(string(runes[index : end+1]))
		}
		index = end
	}
	return strings.Join(strings.Fields(kept.String()), " ")
}

// strippable reports a bracketed group that says nothing about which recording
// this is. An empty group is not one: there is nothing in it to have read.
func strippable(inside string) bool {
	words := strings.FieldsFunc(inside, func(letter rune) bool {
		return !unicode.IsLetter(letter) && !unicode.IsDigit(letter)
	})
	if len(words) == 0 {
		return false
	}
	if producerLabels[strings.ToLower(words[0])] {
		return true
	}
	for _, word := range words {
		if !bracketedLabels[strings.ToLower(word)] {
			return false
		}
	}
	return true
}

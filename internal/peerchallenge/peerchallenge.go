// Package peerchallenge reads the "human check" a few Soulseek peers send by
// private message. Those peers reject or hold every transfer until one word is
// sent back in chat, so a want that meets one fails on a file the peer is
// sharing and would send.
//
// The word is only taken from a shape somebody wrote down here after reading a
// real message. Anything else is refused, and refused messages are shown to the
// person instead. Nothing in this package looks at the music: a file that
// arrives after a reply is proved the same way as any other file.
package peerchallenge

import (
	"regexp"
	"strings"
)

// RequestNew is the one thing besides the word itself that may be sent. The
// paradox1977 bot documents it in its own message: a challenge that ran out is
// replaced by asking for a new one.
const RequestNew = "!challenge"

// maxWordLength bounds what may be sent back. The longest real challenge is the
// six-word phrase delightful asks for, so this is generous. A "word" longer
// than a chat line is not a challenge, it is a parse that ran away.
const maxWordLength = 64

var (
	// labelledWord is the paradox1977 bot: "Reply only with this word /
	// Antworte nur mit diesem Wort: BERLIN." Both spellings end in a colon and
	// the word, so one pattern reads either. It is tried first because that
	// message also quotes the album title, and a title is not a challenge.
	labelledWord = regexp.MustCompile(`(?i)\bwor[dt]\s*:\s*(\S{1,64})`)

	// quotedWord is the ProveIt plugin and DJRolee: type, write, say, reply or
	// respond with the word, in quotes, immediately after the verb. The quotes
	// have to follow the verb, because peers quote album titles and their own rules as well.
	quotedWord = regexp.MustCompile(`(?i)\b(?:(?:type|write|say)\s+|(?:reply|respond)\s+with\s+)["\x{201c}]([^"\x{201d}]{1,64})["\x{201d}]`)

	// namedWord is O A T S on 2026-09-05: ask for the word by naming it
	// after "the word" or "this word", with the word in quotes.
	namedWord = regexp.MustCompile(`(?i)\b(?:the|this)\s+word\s+["\x{201c}]([^"\x{201d}]{1,64})["\x{201d}]`)

	// angledWord is delightful: say, reply or respond > please daddy give it to me < in this
	// chat. The phrase is whatever sits between the two marks.
	angledWord = regexp.MustCompile(`(?i)\b(?:type|write|say|reply|respond)\s*>\s*([^<>]{1,64})\s*<`)

	// expiredChallenge is what paradox1977 answers to a word that came too
	// late. It is the one message that asks for a command back rather than a
	// word.
	expiredChallenge = regexp.MustCompile(`(?i)\bchallenge\b[^.]{0,40}\bexpired\b`)

	// announcements are the messages these peers send that ask for nothing: a
	// word being accepted, a challenge running out, and the two notes PSXDupe
	// sends beside its challenge about what it shares and who it bans. Each was
	// read off a live conversation, and the list is enumerated for the same
	// reason the challenge patterns are — a message nobody wrote down here is
	// shown to a person rather than judged.
	//
	// Recognising them decides nothing about music. It decides whether a
	// message becomes a line on the Downloads page, and the cost of getting it
	// wrong is a note the person does not see, against a reply box after every
	// word that worked.
	announcements = []*regexp.Regexp{
		// paradox1977 accepting a word. The rest of that message counts the
		// queue, which changes every time and defeated the once-per-message
		// rule.
		regexp.MustCompile(`(?i)^\s*correct[.!]`),
		regexp.MustCompile(`(?i)\bdownloads are unlocked\b`),
		// PSXDupe on the collection it shares, and on who it bans.
		regexp.MustCompile(`(?i)\bi spend a great amount of time\b`),
		regexp.MustCompile(`(?i)\bbanned immediately\b`),
	}

	// sentencePunctuation is what ends the sentence a labelled word sits in:
	// "Wort: BERLIN." asks for BERLIN. Only that shape is trimmed. A word
	// inside quotes or between marks is already bounded by them, and trimming
	// there would change a word the peer chose.
	sentencePunctuation = `.,;:!?`
)

// ChallengeWord reports the word one message asks to have sent back, and
// whether the message asks for one at all. The word is returned exactly as the
// peer wrote it, case included, because a peer checking for "O-Hi-O" is
// checking for that and not for something near it.
//
// A message that matches none of the patterns above is refused. Guessing at
// prose would send strangers whatever a sentence happened to contain.
func ChallengeWord(message string) (string, bool) {
	for _, rule := range []struct {
		pattern       *regexp.Regexp
		endsASentence bool
	}{
		{labelledWord, true},
		{quotedWord, false},
		{namedWord, false},
		{angledWord, false},
	} {
		match := rule.pattern.FindStringSubmatch(message)
		if match == nil {
			continue
		}
		word := match[1]
		if rule.endsASentence {
			word = strings.TrimRight(word, sentencePunctuation)
		}
		if word, ok := clean(word); ok {
			return word, true
		}
	}
	return "", false
}

// Expired reports the peer saying the challenge it set has run out. The reply
// to that is RequestNew rather than a word.
func Expired(message string) bool {
	return expiredChallenge.MatchString(message)
}

// Informational reports a message that tells us something rather than asking
// for something. Nothing is sent in answer to one, and it is not shown as a
// question either.
func Informational(message string) bool {
	if Expired(message) {
		return true
	}
	for _, pattern := range announcements {
		if pattern.MatchString(message) {
			return true
		}
	}
	return false
}

// clean trims what surrounded the word in the sentence and refuses what is left
// if it could not be a challenge word: nothing, a chat command, or a line of
// its own. A command is refused because the only command Schall sends is the
// one it knows the meaning of.
func clean(word string) (string, bool) {
	word = strings.TrimSpace(word)
	if word == "" || len(word) > maxWordLength {
		return "", false
	}
	if strings.ContainsAny(word, "\n\r\t") {
		return "", false
	}
	if strings.HasPrefix(word, "!") || strings.HasPrefix(word, "/") {
		return "", false
	}
	if !strings.ContainsFunc(word, func(r rune) bool {
		return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
	}) {
		return "", false
	}
	return word, true
}

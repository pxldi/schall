package peerchallenge

import "testing"

const proveItMessage = `To prove you are a human downloading these files, please type "open sesame" in this chat to be added to my whitelist.`

// Every message below was read from a live slskd conversation on 2026-09-04.
// The peer names are the ones that sent them.
func TestChallengeWordReadsTheMessagesPeersActuallySend(t *testing.T) {
	for name, testCase := range map[string]struct {
		message string
		want    string
	}{
		"ProveIt, Elegy": {
			message: `ProveIt: To prove you are a human downloading these files, please type "download" in this chat to be added to my whitelist.`,
			want:    "download",
		},
		"ProveIt, JohnnyMB": {
			message: `ProveIt: To prove you are a human downloading these files, please type "O-Hi-O" in this chat to be added to my whitelist.`,
			want:    "O-Hi-O",
		},
		"ProveIt, Nazarene1": {
			message: `ProveIt: To prove you are a human downloading these files, please type "rockstar" in this chat to be added to my whitelist.`,
			want:    "rockstar",
		},
		"ProveIt, abuelooorl": {
			message: `ProveIt: To prove you are a human downloading these files, please type "getthefiles" in this chat to be added to my whitelist.`,
			want:    "getthefiles",
		},
		"ProveIt without the prefix, abuelooorl": {
			message: `To prove you are a human downloading these files, please type "getthefiles" in this chat to be added to my whitelist.`,
			want:    "getthefiles",
		},
		"ProveIt with a trailing note, hottie_vs_hoopdee_et_al": {
			message: `ProveIt: To prove you are a human downloading these files, please type "dumptrump" in this chat to be added to my whitelist. This is an automated process.`,
			want:    "dumptrump",
		},
		"ProveIt, PSXDupe": {
			message: `To prove you are a human downloading these files, please type "open sesame" in this chat to be added to my whitelist.`,
			want:    "open sesame",
		},
		"a phrase between marks, delightful": {
			message: `To prove you are a human downloading these files, say > please daddy give it to me < in this chat to be added to my whitelist.`,
			want:    "please daddy give it to me",
		},
		"write the word, DJRolee": {
			message: "Hi! Before downloading, please just write \"DJ Rolee\". This confirms that you're not using an automated downloader. Thank you! \U0001F642",
			want:    "DJ Rolee",
		},
		"the bot, paradox1977, BERLIN": {
			message: `Human check for your requested album "Pashanim (German rapper) / Pashanim - Album - 2022 - Himmel uber Berlin". Reply only with this word / Antworte nur mit diesem Wort: BERLIN. This unlocks downloads for 7 days. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`,
			want:    "BERLIN",
		},
		"the bot, paradox1977, BERLINER": {
			message: `Human check for your requested album "Ufo361 / Ufo361 - Album - 2016 - Ich bin 2 Berliner". Reply only with this word / Antworte nur mit diesem Wort: BERLINER. This unlocks downloads for 7 days. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`,
			want:    "BERLINER",
		},
		"the bot, paradox1977, KLEINSTADT": {
			message: `Human check for your requested album "RIN (German rapper) / RIN - Album - 2021 - Kleinstadt". Reply only with this word / Antworte nur mit diesem Wort: KLEINSTADT. This unlocks downloads for 7 days. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`,
			want:    "KLEINSTADT",
		},
		"the bot, paradox1977, GOD": {
			message: `Human check for your requested album "88GLAM / 88GLAM - Album - 2020 - Close to Heaven Far From God". Reply only with this word / Antworte nur mit diesem Wort: GOD. This unlocks downloads for 7 days. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`,
			want:    "GOD",
		},
		"the bot, paradox1977, ZIPS": {
			message: `Human check for your requested album "Young Dolph (US rapper) / Young Dolph - Album - 2015 - 16 Zips". Reply only with this word / Antworte nur mit diesem Wort: ZIPS. This unlocks downloads for 7 days. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`,
			want:    "ZIPS",
		},
		"the bot, paradox1977, LEVEL": {
			message: `Human check for your requested album "DC THE DON (US rapper) / DC THE DON - Single - 2024 - GOD LEVEL". Reply only with this word / Antworte nur mit diesem Wort: LEVEL. This unlocks downloads for 7 days. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`,
			want:    "LEVEL",
		},
		"O A T S, straight quotes": {
			message: `please reply with the word "oatstraw"`,
			want:    "oatstraw",
		},
		"O A T S, curly quotes": {
			message: "please reply with the word \u201coatstraw\u201d",
			want:    "oatstraw",
		},
		"reply with a phrase": {
			message: `Reply with "hello there" to continue`,
			want:    "hello there",
		},
	} {
		word, ok := ChallengeWord(testCase.message)
		if !ok {
			t.Errorf("%s: no word read", name)
			continue
		}
		if word != testCase.want {
			t.Errorf("%s: word = %q, want %q", name, word, testCase.want)
		}
	}
}

// The other half of every conversation. None of these asks for anything, and a
// reply to one is a message a stranger did not ask for.
func TestChallengeWordRefusesEverythingElse(t *testing.T) {
	for name, message := range map[string]string{
		"PSXDupe on what it shares": `I spend a great amount of time cleaning and verifying the music I download. Ensuring that all tracks are lossless and not upsampled MP3s. I am happy to share these files with anyone who is sharing.`,
		"PSXDupe on its share rule": `However, Please keep in mind, If you are not sharing at least 2,500+ files, then you'll simply be banned immediately.  If you are sharing rubbish just to share, you will be banned immediately. Along with being banned, you will also be set to ignore.`,
		// The word is there in prose, unquoted. Nazarene1's first message
		// carries the same word in a shape that can be read, so nothing is
		// lost by refusing this one.
		"Nazarene1 repeating itself in prose": `Just in case you couldn't download, just type rockstar in lowercase without any punctuation, it's a plugin that I use to stop bots from spamming me.`,
		"paradox1977 accepting a word":        `Correct. Downloads are unlocked for 7 days. Please retry any files that are not already queued; your requested paths remain recorded. For permanent priority access, send !friend. The upload queue is currently busy (300 users waiting). Uploads are scheduled round-robin, so the displayed file position is not an exact waiting position. Please keep your client online and allow time.`,
		"paradox1977 on an expired challenge": `The download challenge expired. Request a file again for a new challenge.`,
		"the Soulseek server on a ban":        `System Message: You have been banned for 30 minutes. This is usually the result of doing too many operations at once. Do not flood chat rooms or private chat with many messages. Do not repeat the same text several times in a row. Do not quickly repeat a search. Do not connect and disconnect quickly. Do not connect many clients at once from the same IP. This list is by no mean exhaustive. You will be able to reconnect to the service in 30 minutes. NOTE: This is an automatic message, dont try to answer it.`,
		"an empty message":                    "",
		"an album title with no request":      `Thanks for downloading "Himmel uber Berlin", enjoy`,
	} {
		if word, ok := ChallengeWord(message); ok {
			t.Errorf("%s: read %q as a challenge word", name, word)
		}
	}
}

// A peer that asks for a command gets nothing. The only command Schall sends is
// RequestNew, and it sends that because it knows what it does.
func TestChallengeWordRefusesACommand(t *testing.T) {
	for _, message := range []string{
		`To prove you are a human, please type "!ban" in this chat.`,
		`Reply only with this word / Antworte nur mit diesem Wort: !friend.`,
		`say > /quit < in this chat`,
	} {
		if word, ok := ChallengeWord(message); ok {
			t.Errorf("read %q as a challenge word in %q", word, message)
		}
	}
}

// The album a peer names is quoted too. Reading a title as the answer would
// send a stranger a release title every time it asked for a word.
func TestChallengeWordDoesNotReadAQuotedAlbumTitle(t *testing.T) {
	message := `Human check for your requested album "Ufo361 - Album - 2016 - Ich bin 2 Berliner". Reply only with this word / Antworte nur mit diesem Wort: BERLINER. Valid for 2880 minutes.`
	word, ok := ChallengeWord(message)
	if !ok || word != "BERLINER" {
		t.Fatalf("word = %q, ok = %v, want BERLINER", word, ok)
	}
}

// The messages these peers send that ask for nothing. Each was read off a live
// conversation, and each used to become a question on the Downloads page with a
// reply box under it.
func TestInformationalRecognisesTheNotesPeersSend(t *testing.T) {
	for name, message := range map[string]string{
		"paradox1977 accepting a word": `Correct. Downloads are unlocked for 7 days. Please retry any files that are not already queued; your requested paths remain recorded. For permanent priority access, send !friend. The upload queue is currently busy (300 users waiting).`,
		"paradox1977 on an expiry":     `The download challenge expired. Request a file again for a new challenge.`,
		"PSXDupe on what it shares":    `I spend a great amount of time cleaning and verifying the music I download. Ensuring that all tracks are lossless and not upsampled MP3s. I am happy to share these files with anyone who is sharing.`,
		"PSXDupe on its share rule":    `However, Please keep in mind, If you are not sharing at least 2,500+ files, then you'll simply be banned immediately.  If you are sharing rubbish just to share, you will be banned immediately. Along with being banned, you will also be set to ignore.`,
	} {
		if !Informational(message) {
			t.Errorf("%s was not recognised as a note", name)
		}
	}
}

// Anything nobody wrote down here is still a person's to read. A challenge most
// of all: a challenge read as a note would never be answered.
func TestInformationalRefusesWhatItWasNotTaughtToRead(t *testing.T) {
	for name, message := range map[string]string{
		"a challenge":            proveItMessage,
		"the bot's own question": `Antworte nur mit diesem Wort: BERLIN. This unlocks downloads for 7 days.`,
		"prose asking for a word": `Just in case you couldn't download, just type rockstar in lowercase ` +
			`without any punctuation, it's a plugin that I use to stop bots from spamming me.`,
		"an empty message": "",
	} {
		if Informational(message) {
			t.Errorf("%s was read as a note", name)
		}
	}
}

func TestExpiredReadsOnlyTheExpiryMessage(t *testing.T) {
	if !Expired(`The download challenge expired. Request a file again for a new challenge.`) {
		t.Error("the expiry message was not recognised")
	}
	// The challenge that sets a deadline says what to do when it passes. It has
	// not passed yet, and answering it with a command instead of the word
	// throws the challenge away.
	if Expired(`Antworte nur mit diesem Wort: BERLIN. Valid for 2880 minutes. If this challenge expires, send !challenge to request a new one.`) {
		t.Error("a live challenge was read as expired")
	}
	if Expired(`Correct. Downloads are unlocked for 7 days.`) {
		t.Error("an accepted word was read as expired")
	}
}

package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// Postgres jsonb has no way to store \u0000, so a tag carrying one has to lose the
// byte rather than lose the document it was part of.
func TestEncodingEvidenceDropsNULFromWhatAFileSaid(t *testing.T) {
	encoded, err := encodeEvidence(&AcquiredFileEvidence{
		Name:     "01.m4a",
		Observed: &ImportTags{Artist: "Niki Istrefi\x00", Title: "Red\x00Armor"},
		Agrees:   []string{},
		Differs:  []string{},
		Problems: []string{},
	})
	if err != nil {
		t.Fatalf("encodeEvidence() error = %v", err)
	}
	if strings.Contains(string(encoded), `\u0000`) {
		t.Fatalf("encoded = %s, want the escape jsonb refuses gone", encoded)
	}

	var decoded AcquiredFileEvidence
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the repaired document does not decode: %v", err)
	}
	if decoded.Observed.Artist != "Niki Istrefi" || decoded.Observed.Title != "RedArmor" {
		t.Fatalf("observed = %+v, want everything but the NUL kept", decoded.Observed)
	}
}

// A tag whose text is literally a backslash followed by u0000 is not a NUL. It
// encodes to an escaped backslash, and cutting that as if it were the escape
// would leave a document Postgres could not parse at all.
func TestEncodingEvidenceKeepsTextThatMerelyLooksLikeTheEscape(t *testing.T) {
	encoded, err := encodeEvidence(&AcquiredFileEvidence{
		Observed: &ImportTags{Title: `Red\u0000Armor`},
		Agrees:   []string{},
		Differs:  []string{},
		Problems: []string{},
	})
	if err != nil {
		t.Fatalf("encodeEvidence() error = %v", err)
	}
	var decoded AcquiredFileEvidence
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the document does not decode: %v", err)
	}
	if decoded.Observed.Title != `Red\u0000Armor` {
		t.Fatalf("title = %q, want the text left exactly as the file wrote it",
			decoded.Observed.Title)
	}
}

// Numbers are not rewritten by the repair. Decoded through interface{} they
// would come back as exponents, which would quietly change every size and
// duration in the document being fixed.
func TestEncodingEvidenceLeavesNumbersAsTheyWereWritten(t *testing.T) {
	encoded, err := encodeEvidence(&AcquiredFileEvidence{
		Observed:  &ImportTags{Title: "Red\x00Armor", DurationMS: 449695},
		Agrees:    []string{},
		Differs:   []string{},
		Problems:  []string{},
		SizeBytes: 79438624,
	})
	if err != nil {
		t.Fatalf("encodeEvidence() error = %v", err)
	}
	if !strings.Contains(string(encoded), "79438624") {
		t.Fatalf("encoded = %s, want the size written as the integer it is", encoded)
	}
	if !strings.Contains(string(encoded), "449695") {
		t.Fatalf("encoded = %s, want the duration written as the integer it is", encoded)
	}
}

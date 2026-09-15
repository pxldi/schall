package soundcloud

import "testing"

// The title observed on the file that prompted this: everything about the track
// is in one string, and the one field SoundCloud states is the uploader.
func TestReadNamingSplitsAnEditIntoItsArtistAndItsRemixer(t *testing.T) {
	naming := ReadNaming("PinkPantheress - Illegal (Abo Edit) [FREE DL]", "Abo")

	if naming.Artist != "PinkPantheress" {
		t.Errorf("artist = %q, want PinkPantheress", naming.Artist)
	}
	if naming.Title != "Illegal (Abo Edit)" {
		t.Errorf("title = %q, want Illegal (Abo Edit)", naming.Title)
	}
	if naming.Remixer != "Abo" {
		t.Errorf("remixer = %q, want Abo", naming.Remixer)
	}
}

// A title that names nobody keeps the uploader as the artist, which is what
// Schall recorded before the split existed.
func TestReadNamingKeepsTheUploaderWhenTheTitleNamesNobody(t *testing.T) {
	naming := ReadNaming("Late Night Set", "Abo")

	if naming.Artist != "Abo" || naming.Title != "Late Night Set" {
		t.Errorf("naming = %+v, want the uploader and the whole title", naming)
	}
	if naming.Remixer != "" {
		t.Errorf("remixer = %q, want nobody", naming.Remixer)
	}
}

// An uploader who puts their own name in front of their own track is the
// artist, not their own remixer.
func TestReadNamingDoesNotMakeTheUploaderTheirOwnRemixer(t *testing.T) {
	naming := ReadNaming("abo - Illegal", "Abo")

	if naming.Artist != "Abo" || naming.Title != "Illegal" || naming.Remixer != "" {
		t.Errorf("naming = %+v, want Abo by Abo with no remixer", naming)
	}
}

// Only the tag on the end goes. A remix named in round brackets is part of what
// the track is called.
func TestReadNamingKeepsRoundBracketsAndDropsSquareOnes(t *testing.T) {
	naming := ReadNaming("PinkPantheress - Illegal (Abo Edit) [FREE DL] [BUY]", "Abo")

	if naming.Title != "Illegal (Abo Edit)" {
		t.Errorf("title = %q, want both tags gone and the edit kept", naming.Title)
	}
}

// A title that is nothing but a tag keeps it, because the alternative is a
// track with no name at all.
func TestReadNamingLeavesATitleThatIsOnlyATag(t *testing.T) {
	naming := ReadNaming("[FREE DL]", "Abo")

	if naming.Title != "[FREE DL]" {
		t.Errorf("title = %q, want it left alone", naming.Title)
	}
}

// Only the first separator splits. What follows is the track's name, dashes and
// all.
func TestReadNamingSplitsOnTheFirstSeparatorOnly(t *testing.T) {
	naming := ReadNaming("PinkPantheress - Illegal - VIP Mix", "Abo")

	if naming.Artist != "PinkPantheress" || naming.Title != "Illegal - VIP Mix" {
		t.Errorf("naming = %+v, want the split at the first separator", naming)
	}
}

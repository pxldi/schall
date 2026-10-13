package musicbrainz

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// "Paranoid Android" on the 1997 UK CD of OK Computer, as MusicBrainz sends it,
// cut to what is read.
func TestRecordingDiscsReadsTheTablesOfContentsOfEachMedium(t *testing.T) {
	client := linksClient(t, `{
		"releases": [
			{"id": "c7569949-0f67-4682-a0d8-75c4290c52dc", "media": [{
				"position": 1, "tracks": [{"position": 2}],
				"discs": [{"id": "rdosqrxASN.ZqF.d.5pPhHtgVyA-", "sectors": 240687,
					"offsets": [150, 21480, 50242]}]
			}]},
			{"id": "db9b4852-505f-4bbc-be4c-e40b1b4a18e6", "media": [{
				"position": 1, "tracks": [{"position": 6}], "discs": []
			}]}
		]
	}`, 200)

	placements, err := client.RecordingDiscs(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("RecordingDiscs() error = %v", err)
	}
	if len(placements) != 1 {
		t.Fatalf("placements = %+v, want only the medium with a disc", placements)
	}
	placement := placements[0]
	if placement.Track != 2 || placement.Medium != 1 || len(placement.Discs) != 1 ||
		placement.Discs[0].Sectors != 240687 || len(placement.Discs[0].Offsets) != 3 {
		t.Errorf("placement = %+v", placement)
	}
}

func TestRecordingsAtDiscTrackReadsOnlyTheMediaCarryingTheDisc(t *testing.T) {
	disc := "rdosqrxASN.ZqF.d.5pPhHtgVyA-"
	want := uuid.MustParse("9f9cf187-d6f9-437f-9d98-d59cdbd52757")
	client := linksClient(t, `{
		"releases": [
			{"media": [
				{"discs": [{"id": "rdosqrxASN.ZqF.d.5pPhHtgVyA-"}],
				 "tracks": [{"position": 1, "recording": {"id": "0e9d2c5b-1111-4b33-8e1a-4c6a7b8c9d0e"}},
				            {"position": 2, "recording": {"id": "9f9cf187-d6f9-437f-9d98-d59cdbd52757"}}]},
				{"discs": [{"id": "other0000000000000000000000-"}],
				 "tracks": [{"position": 2, "recording": {"id": "11111111-2222-4333-8444-555555555555"}}]}
			]},
			{"media": [
				{"discs": [{"id": "rdosqrxASN.ZqF.d.5pPhHtgVyA-"}],
				 "tracks": [{"position": 1, "recording": {"id": "0e9d2c5b-1111-4b33-8e1a-4c6a7b8c9d0e"}}]}
			]}
		]
	}`, 200)

	recordings, err := client.RecordingsAtDiscTrack(context.Background(), disc, 2)
	if err != nil {
		t.Fatalf("RecordingsAtDiscTrack() error = %v", err)
	}
	if len(recordings) != 2 || recordings[0] != want || recordings[1] != uuid.Nil {
		t.Errorf("recordings = %v, want [%s, nil]", recordings, want)
	}
}

func TestRecordingsAtDiscTrackRefusesSomethingThatIsNotADiscID(t *testing.T) {
	client := linksClient(t, `{}`, 200)
	if _, err := client.RecordingsAtDiscTrack(context.Background(), "../recording/x", 1); err == nil {
		t.Fatal("RecordingsAtDiscTrack() error = nil, want a refusal")
	}
}

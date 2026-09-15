package sources

import (
	"fmt"
	"sort"
	"strings"
)

// Ranking weights. They sum to 1.0 so a score reads as a fraction of the best
// possible candidate rather than an opaque number.
const (
	formatWeight       = 0.40
	completenessWeight = 0.25
	availabilityWeight = 0.20
	speedWeight        = 0.10
	consistencyWeight  = 0.05

	// Upload speed, in bytes per second, at which the speed component
	// saturates. Anything faster is not meaningfully better for an album.
	fastUploadSpeed = 5 << 20
)

var losslessExtensions = map[string]bool{
	"flac": true, "wav": true, "aiff": true, "aif": true,
	"ape": true, "wv": true, "alac": true, "dsf": true, "dff": true,
}

// Rank corroborates and scores every candidate against the query, records why
// it scored that way, and orders the results best-first.
//
// Corroboration outranks score, because the two answer different questions and
// only one of them can be wrong about what the music is. A lossless folder from
// a fast peer that matches none of the catalogue's tracks belongs below a
// modest copy that matches all of them. When the query carries no catalogue
// tracks every match is empty and the order falls back to score alone, exactly
// as it did before there was anything to corroborate against.
//
// Ranking is deterministic: equal candidates fall back to track count and then
// to stable identity fields, so the same search always presents the same order.
func Rank(candidates []Candidate, query Query) []Candidate {
	ranked := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if len(candidate.Files) == 0 {
			continue
		}
		candidate.Match = Corroborate(candidate, query)
		candidate.Score, candidate.Reasons = score(candidate, query, Preferences{})
		ranked = append(ranked, candidate)
	}
	order(ranked)
	return ranked
}

// order sorts ranked candidates best-first. It is separate from Rank because
// laying the user's preferences over a ranking re-sorts it without recomputing
// any of the corroboration.
func order(ranked []Candidate) {
	sort.SliceStable(ranked, func(left, right int) bool {
		a, b := ranked[left], ranked[right]
		if a.Match.Confirmed != b.Match.Confirmed {
			return a.Match.Confirmed > b.Match.Confirmed
		}
		if a.Match.Partial != b.Match.Partial {
			return a.Match.Partial > b.Match.Partial
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.TrackCount() != b.TrackCount() {
			return a.TrackCount() > b.TrackCount()
		}
		if a.Username != b.Username {
			return a.Username < b.Username
		}
		return a.Directory < b.Directory
	})
}

func score(candidate Candidate, query Query, preferences Preferences) (float64, []string) {
	reasons := make([]string, 0, 5)
	total := 0.0

	formatScore, formatReason := scoreFormatFor(candidate, preferences)
	total += formatScore * formatWeight
	reasons = append(reasons, formatReason)

	completenessScore, completenessReason := scoreCompleteness(candidate, query)
	total += completenessScore * completenessWeight
	reasons = append(reasons, completenessReason)

	availabilityScore, availabilityReason := scoreAvailability(candidate)
	total += availabilityScore * availabilityWeight
	reasons = append(reasons, availabilityReason)

	if candidate.UploadSpeed > 0 {
		speed := float64(candidate.UploadSpeed) / fastUploadSpeed
		if speed > 1 {
			speed = 1
		}
		total += speed * speedWeight
		reasons = append(reasons, fmt.Sprintf("Peer uploads at %s", formatSpeed(candidate.UploadSpeed)))
	} else {
		reasons = append(reasons, "Peer upload speed is unknown")
	}

	if consistentFormat(candidate) {
		total += consistencyWeight
	} else {
		reasons = append(reasons, "Folder mixes several audio formats")
	}

	return round(total), reasons
}

func scoreFormat(candidate Candidate) (float64, string) {
	format := strings.ToLower(strings.TrimSpace(candidate.Format))
	if losslessExtensions[format] {
		return 1, fmt.Sprintf("Lossless %s", strings.ToUpper(format))
	}
	if format == "" {
		return 0.25, "Audio format could not be determined"
	}
	bitRate := candidate.AverageBitRate
	label := strings.ToUpper(format)
	switch {
	case bitRate == 0:
		return 0.25, fmt.Sprintf("%s at an unknown bitrate", label)
	case bitRate >= 320:
		return 0.75, fmt.Sprintf("%s at %d kbps", label, bitRate)
	case bitRate >= 256:
		return 0.55, fmt.Sprintf("%s at %d kbps", label, bitRate)
	case bitRate >= 192:
		return 0.35, fmt.Sprintf("%s at %d kbps", label, bitRate)
	default:
		return 0.10, fmt.Sprintf("Low bitrate %s at %d kbps", label, bitRate)
	}
}

func scoreCompleteness(candidate Candidate, query Query) (float64, string) {
	found := candidate.TrackCount()
	expected := query.ExpectedTrackCount
	if expected <= 0 {
		return 0.6, fmt.Sprintf("%s offered", pluralTracks(found))
	}
	switch difference := found - expected; {
	case difference == 0:
		return 1, fmt.Sprintf("All %d catalogue tracks present", expected)
	// Oversupply is graded rather than flat. A folder with a couple of extra
	// files is usually the release plus bonus tracks, while one holding half
	// again as many is usually a different edition, a box set, or a whole
	// discography, and acquiring it means sorting it out by hand afterwards.
	case difference > 0 && difference <= 2:
		return 0.75, fmt.Sprintf("%s offered, a little more than the %d-track release",
			pluralTracks(found), expected)
	case difference > 0 && found*2 <= expected*3:
		return 0.55, fmt.Sprintf("%s offered for a %d-track release", pluralTracks(found), expected)
	case difference > 0:
		return 0.3, fmt.Sprintf("%s offered for a %d-track release, likely a different edition",
			pluralTracks(found), expected)
	case difference >= -2:
		return 0.4, fmt.Sprintf("Missing %s of %d", pluralTracks(-difference), expected)
	default:
		return 0.1, fmt.Sprintf("Incomplete: %d of %d tracks", found, expected)
	}
}

func scoreAvailability(candidate Candidate) (float64, string) {
	if candidate.FreeUploadSlot {
		return 1, "Free upload slot"
	}
	if candidate.QueueLength <= 0 {
		return 0.6, "No free slot, but the queue is empty"
	}
	// Queues grow long quickly on Soulseek; decay rather than disqualify.
	return 1 / (1 + float64(candidate.QueueLength)/5),
		fmt.Sprintf("Queued behind %d transfer(s)", candidate.QueueLength)
}

func consistentFormat(candidate Candidate) bool {
	for _, file := range candidate.Files {
		if !strings.EqualFold(file.Extension, candidate.Format) {
			return false
		}
	}
	return true
}

func pluralTracks(count int) string {
	if count == 1 {
		return "1 track"
	}
	return fmt.Sprintf("%d tracks", count)
}

func formatSpeed(bytesPerSecond int64) string {
	const megabyte = 1 << 20
	if bytesPerSecond >= megabyte {
		return fmt.Sprintf("%.1f MB/s", float64(bytesPerSecond)/megabyte)
	}
	return fmt.Sprintf("%d kB/s", bytesPerSecond/1024)
}

func round(value float64) float64 {
	if value > 1 {
		value = 1
	}
	return float64(int(value*1000+0.5)) / 1000
}

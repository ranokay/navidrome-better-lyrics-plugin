package lyric

import (
	"encoding/json"
	"os"
	"testing"
)

type matchingCorpus struct {
	Cases []struct {
		ID        string        `json:"id"`
		Category  string        `json:"category"`
		Label     string        `json:"label"`
		Timing    string        `json:"timing"`
		Evidence  bool          `json:"evidence"`
		Synthetic bool          `json:"synthetic"`
		Request   RecordingInfo `json:"request"`
		Returned  RecordingInfo `json:"returned"`
		Expect    struct {
			Status   MatchStatus `json:"status"`
			AutoSave bool        `json:"autoSave"`
		} `json:"expect"`
	} `json:"cases"`
}

func loadMatchingCorpus(t *testing.T) matchingCorpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/matching/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus matchingCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) < 12 {
		t.Fatalf("corpus has %d cases; expected the expanded comparison set", len(corpus.Cases))
	}
	return corpus
}

func TestRecordingCorpusClassification(t *testing.T) {
	for _, test := range loadMatchingCorpus(t).Cases {
		t.Run(test.ID, func(t *testing.T) {
			got := AssessRecording(test.Request, test.Returned)
			if got.Status != test.Expect.Status {
				t.Fatalf("status = %q reasons=%v, want %q", got.Status, got.Reasons, test.Expect.Status)
			}
			if got.AutoSave != test.Expect.AutoSave {
				t.Fatalf("autoSave = %v, want %v", got.AutoSave, test.Expect.AutoSave)
			}
			if got.Status == MatchValidated && !got.Playable() {
				t.Fatal("validated result must be playable")
			}
			if got.Status == MatchAmbiguous && got.Playable() {
				t.Fatal("ambiguous result must not be automatically playable")
			}
		})
	}
}

func TestDurationAloneNeverProducesMismatch(t *testing.T) {
	request := RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 200}
	returned := RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 400}
	if got := AssessRecording(request, returned); got.Status != MatchAmbiguous {
		t.Fatalf("status = %q, want ambiguous: duration alone cannot prove a different recording", got.Status)
	}
}

func TestExactTitleAndArtistWithoutCorroborationStaysAmbiguous(t *testing.T) {
	request := RecordingInfo{Title: "Song", Artist: "Artist", Duration: 200}
	returned := RecordingInfo{Title: "Song", Artist: "Artist"}
	if got := AssessRecording(request, returned); got.Status != MatchAmbiguous {
		t.Fatalf("status = %q, want ambiguous when no duration or album corroborates", got.Status)
	}
}

func TestMarkerConflictIsMismatch(t *testing.T) {
	request := RecordingInfo{Title: "Song (Live)", Artist: "Artist", Duration: 200}
	returned := RecordingInfo{Title: "Song (Studio)", Artist: "Artist", Duration: 200}
	if got := AssessRecording(request, returned); got.Status != MatchMismatch {
		t.Fatalf("status = %q, want explicit marker conflict to be mismatch", got.Status)
	}
}

func TestContentRatingIsNotRecordingIdentity(t *testing.T) {
	request := RecordingInfo{Title: "Song", Artist: "Artist", Duration: 200}
	returned := RecordingInfo{Title: "Song (Explicit)", Artist: "Artist", Duration: 200}
	if got := AssessRecording(request, returned); got.Status != MatchValidated {
		t.Fatalf("status = %q, want explicit/clean markers to leave identity valid", got.Status)
	}
}

func TestCorpusThresholdIsBoundedByObservedEvidence(t *testing.T) {
	corpus := loadMatchingCorpus(t)
	var correctMax, wrongMin float64
	wrongMin = 1 << 30
	for _, test := range corpus.Cases {
		if test.Synthetic || test.Request.Duration <= 0 || test.Returned.Duration <= 0 {
			continue
		}
		delta := test.Returned.Duration - test.Request.Duration
		if delta < 0 {
			delta = -delta
		}
		switch test.Label {
		case "same":
			if delta > correctMax {
				correctMax = delta
			}
		case "different":
			// Only duration-based wrong rows (no marker/artist conflict) inform
			// the drift ceiling. Marker-only cases are deliberately 1-2 s away.
			if len(versionMarkers(test.Returned.Title, test.Returned.Album)) == 0 && test.Returned.Artist == test.Request.Artist {
				if delta < wrongMin {
					wrongMin = delta
				}
			}
		}
	}
	if correctMax > recordingDriftSeconds {
		t.Fatalf("correct corpus delta %.1fs exceeds validation ceiling %.1fs", correctMax, recordingDriftSeconds)
	}
	if wrongMin <= recordingDriftSeconds {
		t.Fatalf("markerless wrong corpus delta %.1fs is inside the %.1fs validation ceiling; threshold evidence does not support it", wrongMin, recordingDriftSeconds)
	}
}

func TestNumberedTitlePartsStayDistinct(t *testing.T) {
	request := RecordingInfo{Title: "Song (Part 1)", Artist: "Artist", Album: "Album", Duration: 200}
	returned := RecordingInfo{Title: "Song (Part 2)", Artist: "Artist", Album: "Album", Duration: 200}
	if got := AssessRecording(request, returned); got.Status == MatchValidated {
		t.Fatalf("numbered title parts must not validate as the same recording: %#v", got)
	}
}

func TestNumberedPartSurvivesARemovedVersionQualifier(t *testing.T) {
	request := RecordingInfo{Title: "Song (Part 1)", Artist: "Artist", Album: "Album", Duration: 200}
	returned := RecordingInfo{Title: "Song (Part 1) (Live)", Artist: "Artist", Album: "Live", Duration: 200}
	if got := AssessRecording(request, returned); got.Status == MatchValidated {
		t.Fatalf("a one-sided live qualifier must remain ambiguous: %#v", got)
	}
	base := baseTitleTokens(returned.Title)
	if len(base) != 3 || base[0] != "song" || base[1] != "part" || base[2] != "1" {
		t.Fatalf("base = %v, want the numbered part preserved without the live qualifier", base)
	}
}

func TestOneSidedNonOppositeMarkerIsAmbiguousNotMismatch(t *testing.T) {
	request := RecordingInfo{Title: "Song (Live)", Artist: "Artist", Duration: 200}
	returned := RecordingInfo{Title: "Song (Live) (Acoustic)", Artist: "Artist", Duration: 200}
	if got := AssessRecording(request, returned); got.Status != MatchAmbiguous {
		t.Fatalf("status = %q, want ambiguous for an unconfirmed extra qualifier", got.Status)
	}
}

func TestSlashVersionQualifiersCannotValidateCloseWrongRecordings(t *testing.T) {
	for _, returned := range []string{"Song / Live", "Song / Remix", "Song / Radio Edit"} {
		t.Run(returned, func(t *testing.T) {
			request := RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 200}
			got := AssessRecording(request, RecordingInfo{Title: returned, Artist: "Artist", Album: "Album", Duration: 201})
			if got.Status == MatchValidated || got.AutoSave {
				t.Fatalf("returned %q classified %#v, want non-playable/non-save ambiguity", returned, got)
			}
		})
	}
}

func TestMatchingCorpusSummary(t *testing.T) {
	corpus := loadMatchingCorpus(t)
	counts := map[string]int{}
	statuses := map[MatchStatus]int{}
	timing := map[string]int{}
	wrongMatches, misses, synthetic := 0, 0, 0
	for _, test := range corpus.Cases {
		counts[test.Label]++
		statuses[AssessRecording(test.Request, test.Returned).Status]++
		timing[test.Timing]++
		if test.Synthetic {
			synthetic++
		}
		status := AssessRecording(test.Request, test.Returned).Status
		if test.Label == "different" && status == MatchValidated {
			wrongMatches++
		}
		if test.Label == "same" && status != MatchValidated {
			misses++
		}
	}
	t.Logf("corpus total=%d same=%d different=%d uncertain=%d synthetic=%d validated=%d ambiguous=%d mismatch=%d timing_line=%d timing_word=%d timing_plain=%d wrongMatches=%d conservativeMisses=%d",
		len(corpus.Cases), counts["same"], counts["different"], counts["uncertain"], synthetic,
		statuses[MatchValidated], statuses[MatchAmbiguous], statuses[MatchMismatch],
		timing["line"], timing["word"], timing["plain"], wrongMatches, misses)
}

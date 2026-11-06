package ui

// Tests for the pure by-volume download planning logic. The tricky semantics
// exercised here: decimal chapter numbers (no float merging), unnumbered
// extras, unknown volumes, chapter-number resets across volumes, multi-release
// conflicts requiring an explicit choice, and the frozen ID sequence.

import (
	"testing"

	"github.com/darylhjd/mangodex"
)

// planChapter is a small constructor for raw chapter releases in tests.
// Empty vol/chap produce nil API pointers (unknown volume / unnumbered).
func planChapter(id, vol, chap, lang, title, group, published string) *mangodex.Chapter {
	chapter := &mangodex.Chapter{
		ID: id,
		Attributes: mangodex.ChapterAttributes{
			Title:              title,
			TranslatedLanguage: lang,
			PublishAt:          published,
			CreatedAt:          published,
		},
	}
	if vol != "" {
		v := vol
		chapter.Attributes.Volume = &v
	}
	if chap != "" {
		c := chap
		chapter.Attributes.Chapter = &c
	}
	if group != "" {
		chapter.Relationships = []mangodex.Relationship{{
			Type:       mangodex.ScanlationGroupRel,
			Attributes: &mangodex.ScanlationGroupAttributes{Name: group},
		}}
	}
	return chapter
}

func TestParseNumSegments(t *testing.T) {
	cases := []struct {
		input string
		want  []int64
		ok    bool
	}{
		{"10", []int64{10}, true},
		{"010", []int64{10}, true},
		{"10.1", []int64{10, 1}, true},
		{"10.10", []int64{10, 10}, true}, // distinct from 10.1 without floats
		{"10.0.2", []int64{10, 0, 2}, true},
		{"", nil, false},
		{"10.", nil, false},
		{"10a", nil, false},
		{"extra", nil, false},
	}
	for _, tc := range cases {
		got, ok := parseNumSegments(tc.input)
		if ok != tc.ok {
			t.Fatalf("parseNumSegments(%q) ok = %v, want %v", tc.input, ok, tc.ok)
		}
		if ok {
			if len(got) != len(tc.want) {
				t.Fatalf("parseNumSegments(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseNumSegments(%q) = %v, want %v", tc.input, got, tc.want)
				}
			}
		}
	}
}

func TestCompareChapterNumbers(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"9", "10", -1},
		{"10.1", "10.10", -1}, // float parsing would equate these
		{"10.10", "10.2", 1},  // 10 > 2 in the second segment
		{"10", "10.0", -1},    // prefix sorts first, but they stay distinct keys
		{"10.5", "10.5", 0},
		{"extra", "10", 1}, // non-numeric sorts after numeric
	}
	for _, tc := range cases {
		if got := compareChapterNumbers(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareChapterNumbers(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRawChapterStringsAreNeverMerged(t *testing.T) {
	chapters := []*mangodex.Chapter{
		planChapter("id-ten", "1", "10", "en", "", "A", ""),
		planChapter("id-ten-zero", "1", "10.0", "en", "", "B", ""),
		planChapter("id-ten-one", "1", "10.1", "en", "", "C", ""),
	}
	plan := buildVolumePlan(chapters, map[string]int{"en": 0})

	if len(plan.Slots) != 3 {
		t.Fatalf("expected 3 distinct slots for 10 / 10.0 / 10.1, got %d", len(plan.Slots))
	}
	got := []string{plan.Slots[0].Chap, plan.Slots[1].Chap, plan.Slots[2].Chap}
	want := []string{"10", "10.0", "10.1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestMultiReleaseConflictRequiresExplicitChoice(t *testing.T) {
	chapters := []*mangodex.Chapter{
		planChapter("id-c1-old", "1", "1", "en", "Ch.1", "Group A", "2021-01-01T00:00:00+00:00"),
		planChapter("id-c1-new", "1", "1", "en", "Ch.1", "Group B", "2021-02-01T00:00:00+00:00"),
	}
	plan := buildVolumePlan(chapters, map[string]int{"en": 0})

	if len(plan.Slots) != 1 {
		t.Fatalf("expected one conflicted slot, got %d", len(plan.Slots))
	}
	slot := plan.Slots[0]
	if !slot.conflict() || len(slot.Releases) != 2 {
		t.Fatalf("expected conflict with 2 releases, got conflict=%v releases=%d", slot.conflict(), len(slot.Releases))
	}
	if slot.Chosen != -1 {
		t.Fatalf("multi-release slot must start unresolved, got Chosen=%d", slot.Chosen)
	}
	// Releases sort newest publication first; full IDs are preserved.
	if slot.Releases[0].Chapter.ID != "id-c1-new" || slot.Releases[1].Chapter.ID != "id-c1-old" {
		t.Fatalf("release order wrong: %s then %s", slot.Releases[0].Chapter.ID, slot.Releases[1].Chapter.ID)
	}

	// Freezing while unresolved must report the slot and no IDs.
	ids, _, unresolved := freezePlan([]*planSlot{slot})
	if len(ids) != 0 || len(unresolved) != 1 {
		t.Fatalf("freeze should refuse: ids=%v unresolved=%d", ids, len(unresolved))
	}

	// Explicitly choosing the OLDER release must be honoured exactly (no
	// silent replacement with the newest one).
	slot.Chosen = 1
	ids, byID, unresolved := freezePlan([]*planSlot{slot})
	if len(unresolved) != 0 || len(ids) != 1 || ids[0] != "id-c1-old" {
		t.Fatalf("freeze after explicit choice = %v (unresolved %d), want [id-c1-old]", ids, len(unresolved))
	}
	if byID["id-c1-old"] == nil {
		t.Fatal("frozen map must contain the full chapter ID")
	}
}

func TestCrossVolumeResetAndUnknownVolumeOrdering(t *testing.T) {
	chapters := []*mangodex.Chapter{
		planChapter("id-v2-c1", "2", "1", "en", "", "G", ""),
		planChapter("id-v10-c1", "10", "1", "en", "", "G", ""),
		planChapter("id-unk-c5", "", "5", "en", "", "G", ""),
		planChapter("id-v1-c1", "1", "1", "en", "", "G", ""),
		planChapter("id-v1-c2", "1", "2", "en", "", "G", ""),
	}
	plan := buildVolumePlan(chapters, map[string]int{"en": 0})

	gotVols := []string{}
	gotIDs := []string{}
	for _, slot := range plan.Slots {
		gotVols = append(gotVols, slot.Volume)
		gotIDs = append(gotIDs, slot.chosenID())
	}
	wantVols := []string{"1", "1", "2", "10", ""} // chapter 1 resets per volume; unknown last
	for i := range wantVols {
		if gotVols[i] != wantVols[i] {
			t.Fatalf("volume order = %v, want %v", gotVols, wantVols)
		}
	}
	wantIDs := []string{"id-v1-c1", "id-v1-c2", "id-v2-c1", "id-v10-c1", "id-unk-c5"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("id order = %v, want %v", gotIDs, wantIDs)
		}
	}
	// Volume filter dimensions include the unknown volume and sort last.
	wantVolDims := []string{"1", "2", "10", ""}
	if len(plan.Volumes) != len(wantVolDims) {
		t.Fatalf("volume filter dims = %v, want %v", plan.Volumes, wantVolDims)
	}
	for i := range wantVolDims {
		if plan.Volumes[i] != wantVolDims[i] {
			t.Fatalf("volume dims = %v, want %v", plan.Volumes, wantVolDims)
		}
	}
}

func TestUnnumberedExtras(t *testing.T) {
	chapters := []*mangodex.Chapter{
		planChapter("id-bonus-a", "1", "", "en", "Bonus", "G", ""),
		planChapter("id-bonus-b", "1", "", "en", "Bonus", "H", ""), // same extra, another release
		planChapter("id-after", "1", "", "en", "Afterwords", "G", ""),
		planChapter("id-lone-a", "1", "", "en", "", "G", ""), // untitled: never merged
		planChapter("id-lone-b", "1", "", "en", "", "H", ""),
		planChapter("id-c1", "1", "1", "en", "", "G", ""),
	}
	plan := buildVolumePlan(chapters, map[string]int{"en": 0})

	// Numbered first, then titled extras alpha, then untitled extras.
	want := []struct {
		chap     string
		releases int
		conflict bool
		resolved bool
		chosenID string
	}{
		{"1", 1, false, true, "id-c1"},
		{"", 1, false, true, "id-after"},
		{"", 2, true, false, ""}, // "Bonus" conflict, unresolved
		{"", 1, false, true, "id-lone-a"},
		{"", 1, false, true, "id-lone-b"},
	}
	if len(plan.Slots) != len(want) {
		t.Fatalf("got %d slots, want %d", len(plan.Slots), len(want))
	}
	for i, w := range want {
		slot := plan.Slots[i]
		if slot.Chap != w.chap || len(slot.Releases) != w.releases ||
			slot.conflict() != w.conflict || slot.resolved() != w.resolved {
			t.Fatalf("slot %d = chap%q rel%d conflict%v resolved%v, want %+v",
				i, slot.Chap, len(slot.Releases), slot.conflict(), slot.resolved(), w)
		}
	}
	if plan.Slots[2].Releases[0].Chapter.ID != "id-bonus-a" ||
		plan.Slots[2].Releases[1].Chapter.ID != "id-bonus-b" {
		t.Fatal("bonus releases not kept in stable encounter/full-ID order")
	}
	// Resolve the conflict against the second release and verify the frozen
	// sequence downloads exactly the picked version.
	plan.Slots[2].Chosen = 1
	ids, _, unresolved := freezePlan(plan.Slots)
	if len(unresolved) != 0 {
		t.Fatalf("unexpected unresolved slots: %d", len(unresolved))
	}
	wantIDs := []string{"id-c1", "id-after", "id-bonus-b", "id-lone-a", "id-lone-b"}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ids = %v, want %v", ids, wantIDs)
	}
	for i := range wantIDs {
		if ids[i] != wantIDs[i] {
			t.Fatalf("frozen order = %v, want %v", ids, wantIDs)
		}
	}
}

func TestLanguageGroupingAndRank(t *testing.T) {
	chapters := []*mangodex.Chapter{
		planChapter("id-fr", "1", "1", "fr", "", "G", ""),
		planChapter("id-zh", "1", "1", "zh", "", "G", ""),
		planChapter("id-en", "1", "1", "en", "", "G", ""),
	}
	plan := buildVolumePlan(chapters, map[string]int{"en": 0, "zh": 1})

	// Same chapter number in different languages are distinct slots/releases.
	if len(plan.Slots) != 3 {
		t.Fatalf("expected 3 language slots, got %d", len(plan.Slots))
	}
	wantOrder := []string{"id-en", "id-zh", "id-fr"} // configured order, then alpha
	for i, id := range wantOrder {
		if plan.Slots[i].chosenID() != id {
			t.Fatalf("slot %d = %s, want %s", i, plan.Slots[i].chosenID(), id)
		}
	}
	wantDims := []string{"en", "zh", "fr"}
	for i := range wantDims {
		if plan.Languages[i] != wantDims[i] {
			t.Fatalf("language dims = %v, want %v", plan.Languages, wantDims)
		}
	}
}

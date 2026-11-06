package ui

// This file contains the pure (no TUI) logic used to build a "download plan by
// volume". Raw chapters are organised semantically by volume, language and
// chapter identity while preserving the full chapter ID of every release.
//
// Important rules:
//   - Chapter/volume numbers are kept as their ORIGINAL strings for identity
//     purposes. "10", "10.0" and "10.10" are never merged together, and no
//     floating point parsing is involved. Numbers are only parsed into integer
//     segment tuples for ordering.
//   - Multiple uploads of the same (volume, chapter, language) are different
//     RELEASES of one slot and must be explicitly chosen by the user. They are
//     never silently all downloaded, nor silently replaced by one another.
//   - Chapter number resets across volumes are natural because slots are
//     grouped by volume first.
//   - Unnumbered chapters (e.g. extras/oneshots) without a title can never be
//     proven to be the same release, so each one always stays its own slot.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darylhjd/mangodex"
)

const (
	// planUnknownVolume is the raw volume value used when the API does not
	// report a volume. It sorts after every known volume.
	planUnknownVolume = ""

	planUnnumberedChapter = ""

	planNoGroup = "(no group)"
)

// planRelease : One concrete uploaded chapter release. The full, immutable
// chapter UUID is kept here; this is the value handed to the downloader.
type planRelease struct {
	Chapter   *mangodex.Chapter
	Group     string
	PublishAt time.Time
	CreatedAt time.Time
}

// planSlot : One semantic chapter position (volume + chapter + language). It
// may carry several competing releases that the user must choose between.
type planSlot struct {
	Volume   string // Raw volume string; planUnknownVolume when unknown.
	Chap     string // Raw chapter string; planUnnumberedChapter when unnumbered.
	Lang     string
	Numbered bool

	Releases []planRelease
	// Chosen is the selected release index. -1 means "not chosen yet", which is
	// only possible when there are multiple releases. A single-release slot is
	// resolved automatically in buildVolumePlan.
	Chosen int
}

func (s *planSlot) conflict() bool {
	return len(s.Releases) > 1
}

func (s *planSlot) resolved() bool {
	return s.Chosen >= 0 && s.Chosen < len(s.Releases)
}

// displayTitle : Title to show for the slot. When the user has not picked a
// release yet, the newest release's title is used as a preview only.
func (s *planSlot) displayTitle() string {
	i := s.Chosen
	if i < 0 {
		i = 0
	}
	return strings.TrimSpace(s.Releases[i].Chapter.GetTitle())
}

// chosenID : Full chapter ID of the selected release. Empty when unresolved.
func (s *planSlot) chosenID() string {
	if !s.resolved() {
		return ""
	}
	return s.Releases[s.Chosen].Chapter.ID
}

// volumePlan : The fully organised, ordered plan plus filter dimensions.
type volumePlan struct {
	Slots     []*planSlot
	Volumes   []string // Distinct raw volume strings, ordered; "" last.
	Languages []string // Distinct languages, config order then alphabetic.
}

// buildVolumePlan : Organise raw chapters into an ordered volume plan.
func buildVolumePlan(chapters []*mangodex.Chapter, langRank map[string]int) *volumePlan {
	slotMap := map[string]*planSlot{}
	keys := make([]string, 0, len(chapters))
	standalone := 0

	for _, ch := range chapters {
		vol := rawVolume(ch)
		chap := rawChapter(ch)
		lang := ch.Attributes.TranslatedLanguage

		// Identity key. Exact raw strings only - never a parsed number.
		var key string
		switch {
		case chap != planUnnumberedChapter:
			// Numbered chapter: volume + raw chapter + language.
			key = fmt.Sprintf("n\x00%s\x00%s\x00%s", vol, chap, lang)
		case strings.TrimSpace(ch.GetTitle()) != "":
			// Unnumbered but titled: two extras are the same slot only when
			// their titles match exactly.
			key = fmt.Sprintf("e\x00%s\x00%s\x00%s", vol, lang, strings.TrimSpace(ch.GetTitle()))
		default:
			// Unnumbered, untitled: identity cannot be established, so each
			// upload is its own slot and can never be silently merged.
			key = fmt.Sprintf("u\x00%s\x00%s\x00%d", vol, lang, standalone)
			standalone++
		}

		slot, ok := slotMap[key]
		if !ok {
			slot = &planSlot{
				Volume:   vol,
				Chap:     chap,
				Lang:     lang,
				Numbered: chap != planUnnumberedChapter,
				Chosen:   -1,
			}
			slotMap[key] = slot
			keys = append(keys, key)
		}
		slot.Releases = append(slot.Releases, planRelease{
			Chapter:   ch,
			Group:     chapterScanGroupName(ch),
			PublishAt: parseChapterTime(ch.Attributes.PublishAt),
			CreatedAt: parseChapterTime(ch.Attributes.CreatedAt),
		})
	}

	slots := make([]*planSlot, 0, len(keys))
	for _, key := range keys {
		slots = append(slots, slotMap[key])
	}

	// Order releases inside every slot (newest publication first) and resolve
	// single-release slots. Multi-release slots stay unresolved until the user
	// makes an explicit choice.
	for _, slot := range slots {
		sort.SliceStable(slot.Releases, func(i, j int) bool {
			return releaseLess(slot.Releases[i], slot.Releases[j])
		})
		if len(slot.Releases) == 1 {
			slot.Chosen = 0
		}
	}

	sort.SliceStable(slots, func(i, j int) bool {
		return slotLess(slots[i], slots[j], langRank)
	})

	plan := &volumePlan{Slots: slots}

	volSet := map[string]struct{}{}
	langSet := map[string]struct{}{}
	for _, slot := range slots {
		volSet[slot.Volume] = struct{}{}
		langSet[slot.Lang] = struct{}{}
	}
	for vol := range volSet {
		plan.Volumes = append(plan.Volumes, vol)
	}
	sort.SliceStable(plan.Volumes, func(i, j int) bool {
		return compareVolume(plan.Volumes[i], plan.Volumes[j]) < 0
	})
	for lang := range langSet {
		plan.Languages = append(plan.Languages, lang)
	}
	sort.SliceStable(plan.Languages, func(i, j int) bool {
		ri, rj := languageRank(plan.Languages[i], langRank), languageRank(plan.Languages[j], langRank)
		if ri != rj {
			return ri < rj
		}
		return plan.Languages[i] < plan.Languages[j]
	})

	return plan
}

// freezePlan : Produce the immutable, ordered full chapter-ID sequence for the
// given slots. Returns the unresolved slots as well; a non-empty unresolved
// slice means the caller must NOT start any download.
func freezePlan(slots []*planSlot) ([]string, map[string]*mangodex.Chapter, []*planSlot) {
	ids := make([]string, 0, len(slots))
	byID := make(map[string]*mangodex.Chapter, len(slots))
	var unresolved []*planSlot

	for _, slot := range slots {
		if !slot.resolved() {
			unresolved = append(unresolved, slot)
			continue
		}
		chapter := slot.Releases[slot.Chosen].Chapter
		if _, ok := byID[chapter.ID]; !ok {
			byID[chapter.ID] = chapter
		}
		ids = append(ids, chapter.ID)
	}
	return ids, byID, unresolved
}

// rawVolume : Exact, trimmed volume string; empty when the volume is unknown.
func rawVolume(ch *mangodex.Chapter) string {
	if ch.Attributes.Volume == nil {
		return planUnknownVolume
	}
	return strings.TrimSpace(*ch.Attributes.Volume)
}

// rawChapter : Exact, trimmed chapter number string; empty when unnumbered.
func rawChapter(ch *mangodex.Chapter) string {
	if ch.Attributes.Chapter == nil {
		return planUnnumberedChapter
	}
	return strings.TrimSpace(*ch.Attributes.Chapter)
}

// chapterScanGroupName : Name of the chapter's scanlation group, if present.
func chapterScanGroupName(ch *mangodex.Chapter) string {
	for _, relation := range ch.Relationships {
		if relation.Type == mangodex.ScanlationGroupRel {
			if attrs, ok := relation.Attributes.(*mangodex.ScanlationGroupAttributes); ok {
				return attrs.Name
			}
		}
	}
	return ""
}

// parseChapterTime : Parse MangaDex RFC3339 timestamps; zero time on failure.
func parseChapterTime(value string) time.Time {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

// releaseLess : Newest publication wins; ties fall back to creation time, then
// group name, then full chapter ID for deterministic output.
func releaseLess(a, b planRelease) bool {
	if !a.PublishAt.Equal(b.PublishAt) {
		return a.PublishAt.After(b.PublishAt)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return a.Chapter.ID < b.Chapter.ID
}

// slotLess : Ordering of slots: volume, then language, then numbered chapters
// ascending followed by unnumbered extras ordered by title.
func slotLess(a, b *planSlot, langRank map[string]int) bool {
	if cmp := compareVolume(a.Volume, b.Volume); cmp != 0 {
		return cmp < 0
	}
	if a.Lang != b.Lang {
		ra, rb := languageRank(a.Lang, langRank), languageRank(b.Lang, langRank)
		if ra != rb {
			return ra < rb
		}
		return a.Lang < b.Lang
	}
	if a.Numbered != b.Numbered {
		return a.Numbered // Numbered chapters come before unnumbered extras.
	}
	if a.Numbered {
		if cmp := compareChapterNumbers(a.Chap, b.Chap); cmp != 0 {
			return cmp < 0
		}
	} else {
		ta, tb := strings.ToLower(a.displayTitle()), strings.ToLower(b.displayTitle())
		if ta != tb {
			// Untitled extras are indistinguishable by name, so they trail
			// the titled extras instead of jumping ahead of them.
			if ta == "" {
				return false
			}
			if tb == "" {
				return true
			}
			return ta < tb
		}
	}
	if !a.Releases[0].PublishAt.Equal(b.Releases[0].PublishAt) {
		return a.Releases[0].PublishAt.Before(b.Releases[0].PublishAt)
	}
	return a.Releases[0].Chapter.ID < b.Releases[0].Chapter.ID
}

func languageRank(lang string, ranks map[string]int) int {
	if rank, ok := ranks[lang]; ok {
		return rank
	}
	// Languages not in the user's configuration sort after configured ones.
	return 1<<31 - 1
}

// compareVolume : Compare raw volume strings. Unknown volumes sort last.
// Numeric tuples sort numerically ("2" < "10"); anything else falls back to a
// lexicographic comparison, and numeric volumes always precede non-numeric.
func compareVolume(a, b string) int {
	switch {
	case a == planUnknownVolume && b == planUnknownVolume:
		return 0
	case a == planUnknownVolume:
		return 1
	case b == planUnknownVolume:
		return -1
	}
	return compareNumericOrText(a, b)
}

// compareChapterNumbers : Compare raw chapter numbers without using floats.
// Numeric tuples sort numerically, so "10.1" < "10.10" (1 < 10 in the second
// segment) while the two strings still remain DISTINCT identity values.
func compareChapterNumbers(a, b string) int {
	return compareNumericOrText(a, b)
}

// compareNumericOrText : Compare dotted integer tuples numerically, falling
// back to a case-insensitive lexicographic comparison for non-numeric values.
func compareNumericOrText(a, b string) int {
	sa, oka := parseNumSegments(a)
	sb, okb := parseNumSegments(b)
	switch {
	case oka && okb:
		return compareSegments(sa, sb)
	case oka:
		return -1 // Numeric sorts before non-numeric.
	case okb:
		return 1
	}
	switch {
	case strings.EqualFold(a, b):
		return 0
	case strings.ToLower(a) < strings.ToLower(b):
		return -1
	default:
		return 1
	}
}

// parseNumSegments : Parse "10", "010", "10.5", "10.10" into integer segments.
// Floating point is deliberately never used. Returns false for anything that
// is not a dotted sequence of plain integers.
func parseNumSegments(s string) ([]int64, bool) {
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	segments := make([]int64, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, false
		}
		segments = append(segments, n)
	}
	return segments, true
}

// compareSegments : Lexicographic comparison of integer tuples. A tuple that is
// a strict prefix sorts first ("10" < "10.0").
func compareSegments(a, b []int64) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

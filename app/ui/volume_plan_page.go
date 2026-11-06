package ui

// Volume download plan page. It shows the raw chapters of the manga page
// organised by volume -> language -> semantic chapter, in download order.
// Slots with more than one release are marked as conflicts and the user must
// pick the exact release (full chapter ID) before the plan can be confirmed.
// Only after confirmation is the immutable ID sequence handed to the
// downloader, so downloaded files always match what was previewed.

import (
	"fmt"
	"strings"
	"time"

	"github.com/darylhjd/mangadesk/app/core"
	"github.com/darylhjd/mangadesk/app/ui/utils"
	"github.com/darylhjd/mangodex"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	// planAllFilter is the dropdown value meaning "no filter on this dimension".
	planAllFilter = "*"
)

// VolumePlanPage : Page that previews the by-volume download plan.
type VolumePlanPage struct {
	manga *MangaPage
	plan  *volumePlan

	Grid  *tview.Grid
	Info  *tview.TextView
	Form  *tview.Form
	Table *tview.Table

	volValues  []string // Raw volume values backing the dropdown; "" is the unknown volume.
	langValues []string
	volFilter  string
	langFilter string

	slots []*planSlot // Slots in the current filtered scope, in plan order.
}

// ctrlVInput : Manga page entry point for the by-volume download plan.
func (p *MangaPage) ctrlVInput() {
	ShowVolumePlanPage(p)
}

// collectTableChapters : Gather every chapter currently loaded on the manga
// page table via its (immutable) cell reference.
func (p *MangaPage) collectTableChapters() []*mangodex.Chapter {
	chapters := make([]*mangodex.Chapter, 0, p.Table.GetRowCount())
	for row := 1; row < p.Table.GetRowCount(); row++ {
		if chapter, ok := p.Table.GetCell(row, 0).GetReference().(*mangodex.Chapter); ok {
			chapters = append(chapters, chapter)
		}
	}
	return chapters
}

// ShowVolumePlanPage : Build and show the volume download plan page.
func ShowVolumePlanPage(mangaPage *MangaPage) {
	chapters := mangaPage.collectTableChapters()
	if len(chapters) == 0 {
		modal := okModal(utils.GenericAPIErrorModalID,
			"No chapters available for a plan yet.\nWait for the chapter list to finish loading.")
		ShowModal(utils.GenericAPIErrorModalID, modal)
		return
	}

	ranks := map[string]int{}
	for i, lang := range core.App.Config.Languages {
		ranks[lang] = i
	}
	plan := buildVolumePlan(chapters, ranks)

	page := newVolumePlanPage(mangaPage, plan)
	core.App.PageHolder.AddAndSwitchToPage(utils.VolumePlanPageID, page.Grid, true)
	core.App.TView.SetFocus(page.Table)
}

// newVolumePlanPage : Construct the plan page UI.
func newVolumePlanPage(mangaPage *MangaPage, plan *volumePlan) *VolumePlanPage {
	var dimensions []int
	for i := 0; i < 15; i++ {
		dimensions = append(dimensions, -1)
	}
	grid := utils.NewGrid(dimensions, dimensions)
	grid.SetTitleColor(utils.PlanPageGridTitleColor).
		SetBorderColor(utils.PlanPageGridBorderColor).
		SetTitle("Download Plan by Volume").
		SetBorder(true)

	info := tview.NewTextView()
	info.SetWrap(true).SetWordWrap(true).
		SetDynamicColors(true).
		SetBorderColor(utils.PlanPageGridBorderColor).
		SetTitleColor(utils.PlanPageTableTitleColor).
		SetTitle("Plan Summary & Conflicts").
		SetBorder(true)

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignLeft).
		SetLabelColor(utils.SearchFormLabelColor)

	table := tview.NewTable()
	table.SetSelectable(true, false).
		SetSeparator('|').
		SetBordersColor(utils.PlanPageTableBorderColor).
		SetTitleColor(utils.PlanPageTableTitleColor).
		SetTitle("Planned download order").
		SetBorder(true)

	grid.AddItem(info, 0, 0, 5, 15, 0, 0, false).
		AddItem(form, 5, 0, 3, 15, 0, 0, false).
		AddItem(table, 8, 0, 7, 15, 0, 0, true).
		AddItem(info, 0, 0, 15, 5, 0, 72, false).
		AddItem(form, 0, 5, 15, 4, 0, 10, true).
		AddItem(table, 0, 9, 15, 6, 0, 80, true)

	page := &VolumePlanPage{
		manga:      mangaPage,
		plan:       plan,
		Grid:       grid,
		Info:       info,
		Form:       form,
		Table:      table,
		volFilter:  planAllFilter,
		langFilter: planAllFilter,
	}

	// Volume dropdown (first entry is "all"; raw "" is the unknown volume).
	page.volValues = append([]string{planAllFilter}, plan.Volumes...)
	volOptions := make([]string, len(page.volValues))
	for i, value := range page.volValues {
		if i == 0 {
			volOptions[i] = "All volumes"
		} else {
			volOptions[i] = displayVolume(value)
		}
	}
	form.AddDropDown("Volume:", volOptions, 0, func(_ string, index int) {
		page.volFilter = page.volValues[index]
		page.applyFilters()
	})

	// Language dropdown.
	page.langValues = append([]string{planAllFilter}, plan.Languages...)
	langOptions := make([]string, len(page.langValues))
	for i, value := range page.langValues {
		if i == 0 {
			langOptions[i] = "All languages"
		} else {
			langOptions[i] = value
		}
	}
	form.AddDropDown("Language:", langOptions, 0, func(_ string, index int) {
		page.langFilter = page.langValues[index]
		page.applyFilters()
	})

	form.AddButton("Choose Release", page.chooseForCurrentRow).
		AddButton("Confirm Download", page.confirmPlan).
		AddButton("Cancel", page.close)

	page.setHandlers()
	page.applyFilters()

	return page
}

// setHandlers : Keyboard handlers for the plan page.
func (p *VolumePlanPage) setHandlers() {
	p.Grid.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEsc {
			p.close()
		}
		return event
	})

	// Down/Tab from the filter form moves into the table.
	p.Form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyDown || event.Key() == tcell.KeyTab {
			core.App.TView.SetFocus(p.Table)
			return nil
		}
		return event
	})

	// Tab from the table moves back to the filter/action form.
	p.Table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			core.App.TView.SetFocus(p.Form)
			return nil
		}
		return event
	})

	// Enter on a row opens the explicit release chooser for that slot.
	p.Table.SetSelectedFunc(func(_, _ int) {
		p.chooseForCurrentRow()
	})
}

// close : Leave the plan page without downloading anything.
func (p *VolumePlanPage) close() {
	core.App.PageHolder.RemovePage(utils.VolumePlanPageID)
}

// applyFilters : Recompute the visible scope from the dropdown selections.
func (p *VolumePlanPage) applyFilters() {
	p.slots = p.slots[:0]
	for _, slot := range p.plan.Slots {
		if p.volFilter != planAllFilter && slot.Volume != p.volFilter {
			continue
		}
		if p.langFilter != planAllFilter && slot.Lang != p.langFilter {
			continue
		}
		p.slots = append(p.slots, slot)
	}
	p.renderInfo()
	p.renderTable()
}

// renderInfo : Show plan totals, scope and unresolved conflicts.
func (p *VolumePlanPage) renderInfo() {
	var (
		totalReleases    int
		totalConflicts   int
		globalUnresolved int
	)
	for _, slot := range p.plan.Slots {
		totalReleases += len(slot.Releases)
		if slot.conflict() {
			totalConflicts++
		}
		if !slot.resolved() {
			globalUnresolved++
		}
	}

	scopeUnresolved := 0
	for _, slot := range p.slots {
		if !slot.resolved() {
			scopeUnresolved++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Manga: [white]%s\n", tview.Escape(p.manga.Manga.GetTitle("en")))
	fmt.Fprintf(&b, "Plan slots: [white]%d[white] from [white]%d[white] uploaded releases; conflicting slots: [white]%d[white].\n",
		len(p.plan.Slots), totalReleases, totalConflicts)
	fmt.Fprintf(&b, "Current scope: [white]%d[white] slot(s); unresolved conflicts in scope: [%s]%d[white].\n",
		len(p.slots), conflictColorName(scopeUnresolved), scopeUnresolved)
	fmt.Fprintf(&b, "Order: volume -> language -> numbered chapters ascending -> unnumbered extras. ")
	fmt.Fprintf(&b, "Unknown volumes sort last; chapter numbers reset per volume and are never merged via floats.\n")
	fmt.Fprintf(&b, "[yellow]Enter[white] on a [red]red CHOOSE[white] row to select the exact release. ")
	fmt.Fprintf(&b, "[yellow]Tab[white] toggles filters/table, [yellow]Esc[white] cancels. ")
	fmt.Fprintf(&b, "Only the chosen full chapter IDs are downloaded, in the order shown.\n")
	if globalUnresolved > 0 {
		fmt.Fprintf(&b, "\n[%s]%d conflict(s) still need an explicit release choice.", conflictColorName(globalUnresolved), globalUnresolved)
	}

	p.Info.SetText(b.String())
}

// renderTable : Rebuild the plan preview table in download order.
func (p *VolumePlanPage) renderTable() {
	selectedRow, _ := p.Table.GetSelection()

	p.Table.Clear()
	headers := []string{"#", "Vol", "Chap", "Lang", "Name", "Picked Release", "Status"}
	for col, header := range headers {
		p.Table.SetCell(0, col, tview.NewTableCell(header).
			SetTextColor(utils.PlanPageTableTitleColor).
			SetSelectable(false))
	}
	p.Table.SetFixed(1, 0)

	for i, slot := range p.slots {
		row := i + 1

		seqCell := tview.NewTableCell(fmt.Sprintf("%-4d", i+1)).
			SetTextColor(utils.PlanPageSeqColor).
			SetReference(slot)
		volCell := tview.NewTableCell(fmt.Sprintf("%-5s", displayVolume(slot.Volume))).
			SetMaxWidth(7).SetTextColor(utils.MangaPageChapNumColor)
		chapCell := tview.NewTableCell(fmt.Sprintf("%-7s", displayChapter(slot.Chap))).
			SetMaxWidth(9).SetTextColor(utils.MangaPageChapNumColor)
		langCell := tview.NewTableCell(slot.Lang).SetTextColor(utils.MangaPageReadStatColor)
		nameCell := tview.NewTableCell(fmt.Sprintf("%-30s", tview.Escape(slot.displayTitle()))).
			SetMaxWidth(30).SetTextColor(utils.MangaPageTitleColor)

		var (
			pickedText  string
			statusText  string
			statusColor = utils.PlanPageOKColor
		)
		if slot.resolved() {
			release := slot.Releases[slot.Chosen]
			group := release.Group
			if group == "" {
				group = planNoGroup
			}
			pickedText = fmt.Sprintf("%-30s [%s]", group, shortChapterID(release.Chapter.ID))
			statusText = "OK"
		} else {
			statusText = fmt.Sprintf("CHOOSE (%d releases)", len(slot.Releases))
			statusColor = utils.PlanPageConflictColor
		}
		pickedCell := tview.NewTableCell(pickedText).
			SetMaxWidth(42).SetTextColor(utils.PlanPagePickedColor)
		statusCell := tview.NewTableCell(statusText).SetTextColor(statusColor)

		p.Table.SetCell(row, 0, seqCell).
			SetCell(row, 1, volCell).
			SetCell(row, 2, chapCell).
			SetCell(row, 3, langCell).
			SetCell(row, 4, nameCell).
			SetCell(row, 5, pickedCell).
			SetCell(row, 6, statusCell)
	}

	if p.Table.GetRowCount() > 1 {
		if selectedRow < 1 || selectedRow >= p.Table.GetRowCount() {
			selectedRow = 1
		}
		p.Table.Select(selectedRow, 0)
	}
}

// currentSlot : Slot referenced by the currently selected table row, if any.
func (p *VolumePlanPage) currentSlot() *planSlot {
	row, _ := p.Table.GetSelection()
	cell := p.Table.GetCell(row, 0)
	if cell == nil {
		return nil
	}
	slot, ok := cell.GetReference().(*planSlot)
	if !ok {
		return nil
	}
	return slot
}

// chooseForCurrentRow : Open the release chooser for the highlighted slot.
func (p *VolumePlanPage) chooseForCurrentRow() {
	slot := p.currentSlot()
	if slot == nil {
		return
	}
	p.openReleaseChooser(slot)
}

// confirmPlan : Gate confirmation on explicit conflict resolution, then freeze
// the immutable ID sequence and hand it to the downloader.
func (p *VolumePlanPage) confirmPlan() {
	if len(p.slots) == 0 {
		modal := okModal(utils.VolumePlanConflictModalID, "Current scope is empty.\nAdjust the volume/language filters.")
		ShowModal(utils.VolumePlanConflictModalID, modal)
		return
	}

	ids, byID, unresolved := freezePlan(p.slots)
	if len(unresolved) > 0 {
		modal := okModal(utils.VolumePlanConflictModalID, fmt.Sprintf(
			"%d slot(s) in the current scope have multiple releases and no explicit choice yet.\n"+
				"Open each red CHOOSE row with Enter and pick one release.\n"+
				"Nothing will be downloaded until every conflict is resolved.", len(unresolved)))
		ShowModal(utils.VolumePlanConflictModalID, modal)
		return
	}

	text := fmt.Sprintf(
		"Start the planned download of %d chapter(s) in the order shown?\n"+
			"Unresolved conflicts: 0.\n"+
			"The confirmed full chapter-ID sequence is fixed for this download.",
		len(ids))
	modal := confirmModal(utils.VolumePlanConfirmModalID, text, "Download", func() {
		// Immutable snapshot captured here: only IDs are passed downstream.
		frozenIDs := append([]string(nil), ids...)
		p.close()
		go p.manga.downloadChapterIDs(frozenIDs, byID, 0)
	})
	ShowModal(utils.VolumePlanConfirmModalID, modal)
}

// openReleaseChooser : Overlay listing every release of one slot with its scan
// group, timestamps and FULL chapter ID. The user must pick explicitly.
func (p *VolumePlanPage) openReleaseChooser(slot *planSlot) {
	header := tview.NewTextView()
	header.SetWrap(true).SetWordWrap(true).
		SetDynamicColors(true).
		SetBorder(true).
		SetTitleColor(utils.PlanPageTableTitleColor).
		SetBorderColor(utils.PlanPageGridBorderColor)
	header.SetText(fmt.Sprintf("Select exact release — Vol [white]%s[white] · Chap [white]%s[white] · [white]%s\n"+
		"%d release(s) found for this slot. The full chapter ID identifies the version that will be downloaded.",
		tview.Escape(displayVolume(slot.Volume)), tview.Escape(displayChapter(slot.Chap)),
		tview.Escape(slot.Lang), len(slot.Releases)))

	list := tview.NewList().ShowSecondaryText(true).SetWrapAround(true)
	for i, release := range slot.Releases {
		i := i
		group := release.Group
		if group == "" {
			group = planNoGroup
		}
		prefix := "   "
		if i == slot.Chosen {
			prefix = "[green]>> [white]"
		}
		main := fmt.Sprintf("%s%d. %s", prefix, i+1, tview.Escape(group))
		secondary := fmt.Sprintf("published %s | uploaded %s | ID %s",
			displayTime(release.PublishAt), displayTime(release.CreatedAt), release.Chapter.ID)
		list.AddItem(main, secondary, 0, func() {
			slot.Chosen = i
			core.App.PageHolder.RemovePage(utils.ReleaseChooserID)
			p.renderInfo()
			p.renderTable()
			core.App.TView.SetFocus(p.Table)
		})
	}
	list.AddItem("   Cancel", "Keep the current choice and go back", 0, func() {
		core.App.PageHolder.RemovePage(utils.ReleaseChooserID)
		core.App.TView.SetFocus(p.Table)
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEsc {
			core.App.PageHolder.RemovePage(utils.ReleaseChooserID)
			core.App.TView.SetFocus(p.Table)
			return nil
		}
		return event
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 6, 0, false).
		AddItem(list, 0, 1, true)
	flex.SetBorder(true).
		SetTitle("Release Version - choose one").
		SetBorderColor(utils.PlanPageGridBorderColor).
		SetTitleColor(utils.PlanPageGridTitleColor)

	core.App.PageHolder.AddAndSwitchToPage(utils.ReleaseChooserID, flex, true)
	core.App.TView.SetFocus(list)
}

// displayVolume : Human-readable volume label.
func displayVolume(volume string) string {
	if volume == planUnknownVolume {
		return "?"
	}
	return volume
}

// displayChapter : Human-readable chapter label.
func displayChapter(chapter string) string {
	if chapter == planUnnumberedChapter {
		return "extra"
	}
	return chapter
}

// displayTime : Compact timestamp label; "unknown" when unavailable.
func displayTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("2006-01-02")
}

// shortChapterID : Short ID shown in previews; the downloader still receives
// and uses the complete chapter UUID.
func shortChapterID(id string) string {
	return strings.SplitN(id, "-", 2)[0]
}

// conflictColorName : tview color tag for conflict counts.
func conflictColorName(count int) string {
	if count > 0 {
		return "red"
	}
	return "green"
}

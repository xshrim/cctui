package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"cctui/internal/ccswitch"
	"cctui/internal/codex"
	"cctui/internal/grok"
	"cctui/internal/pi"
)

type screenMode int

const (
	modeList screenMode = iota
	modeForm
	modeConfirm
	modeSwitchConfirm
)

type rowKind int

const (
	rowHeading rowKind = iota
	rowSpacer
	rowProvider
	rowAdd
)

type statusLevel int

const (
	statusInfo statusLevel = iota
	statusSuccess
	statusError
)

type listRow struct {
	kind     rowKind
	app      ccswitch.AppType
	provider *ccswitch.Provider
	key      string
}

type formState struct {
	app          ccswitch.AppType
	editMode     bool
	original     *ccswitch.Provider
	fields       []textinput.Model
	labels       []string
	focusIndex   int
	errorMessage string
}

type confirmState struct {
	app      ccswitch.AppType
	provider ccswitch.Provider
}

type switchConfirmState struct {
	app            ccswitch.AppType
	provider       ccswitch.Provider
	restoreSession bool
	preview        *ccswitch.SwitchPreview
	diffOffset     int
}

type Model struct {
	store         *ccswitch.Store
	width         int
	height        int
	mode          screenMode
	rows          []listRow
	cursor        int
	current       map[ccswitch.AppType]string
	providers     map[ccswitch.AppType][]ccswitch.Provider
	form          formState
	confirm       *confirmState
	switchConfirm *switchConfirmState
	status        string
	statusKind    statusLevel
	selectedKey   string
}

var (
	titleStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	groupStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69"))
	currentStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	currentRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42")).
			Bold(true)
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("63")).
			Bold(true)
	addRowStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	helpKeyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	mutedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	successStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	panelStyle      = lipgloss.NewStyle().Border(lipgloss.ASCIIBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2)
	labelStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("69")).Bold(true)
	badgeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1).Bold(true)
	headerMetaStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	statusBarStyle  = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)
	formHintStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	panelTitleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	dangerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	diffAddStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	diffRemoveStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	diffHeaderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	diffContextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selectedAddStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("63")).
				Bold(true)
)

func NewModel(store *ccswitch.Store, warnings []string) (*Model, error) {
	snapshot, err := store.Snapshot()
	if err != nil {
		return nil, err
	}

	model := &Model{
		store:     store,
		width:     100,
		height:    32,
		mode:      modeList,
		current:   snapshot.Current,
		providers: snapshot.Providers,
	}
	model.rebuildRows()
	if len(warnings) > 0 {
		model.setStatus(strings.Join(warnings, "；"), statusInfo)
	} else {
		model.setStatus("就绪", statusInfo)
	}

	return model, nil
}

func (m *Model) Init() tea.Cmd {
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		return m, nil
	}

	switch m.mode {
	case modeList:
		return m.updateList(msg)
	case modeForm:
		return m.updateForm(msg)
	case modeConfirm:
		return m.updateConfirm(msg)
	case modeSwitchConfirm:
		return m.updateSwitchConfirm(msg)
	default:
		return m, nil
	}
}

func (m *Model) View() string {
	switch m.mode {
	case modeForm:
		return m.viewForm()
	case modeConfirm:
		return m.viewConfirm()
	case modeSwitchConfirm:
		return m.viewSwitchConfirm()
	default:
		return m.viewList()
	}
}

func (m *Model) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.KeyMsg:
		switch typed.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			m.moveCursor(-1)
		case "down", "j":
			m.moveCursor(1)
		case "g":
			m.moveToEdge(true)
		case "G":
			m.moveToEdge(false)
		case "1":
			m.jumpToApp(ccswitch.AppClaude)
		case "2":
			m.jumpToApp(ccswitch.AppCodex)
		case "3":
			m.jumpToApp(ccswitch.AppGemini)
		case "4":
			m.jumpToApp(ccswitch.AppGrok)
		case "5":
			m.jumpToApp(ccswitch.AppOpencode)
		case "6":
			m.jumpToApp(ccswitch.AppPi)
		case "a":
			row := m.selectedRow()
			if row != nil {
				m.openAddForm(row.app)
				return m, textinput.Blink
			}
		case "e":
			row := m.selectedRow()
			if row != nil && row.kind == rowProvider && row.provider != nil {
				m.openEditForm(row.app, *row.provider)
				return m, textinput.Blink
			}
		case "d":
			row := m.selectedRow()
			if row != nil && row.kind == rowProvider && row.provider != nil {
				m.confirm = &confirmState{app: row.app, provider: *row.provider}
				m.mode = modeConfirm
			}
		case "enter":
			row := m.selectedRow()
			if row == nil {
				return m, nil
			}
			switch row.kind {
			case rowAdd:
				m.openAddForm(row.app)
				return m, textinput.Blink
			case rowProvider:
				if row.provider == nil {
					return m, nil
				}
				if row.app.IsIncremental() {
					// 增量模式不支持切换，回车进入编辑
					m.openEditForm(row.app, *row.provider)
					return m, nil
				}
				if m.current[row.app] == row.provider.ID {
					m.setStatus(fmt.Sprintf("%s 已经是当前供应商", row.provider.Name), statusInfo)
					return m, nil
				}
				preview, err := m.store.PreviewSwitch(row.app, row.provider.ID)
				if err != nil {
					m.setStatus(fmt.Sprintf("切换预览失败: %v", err), statusError)
					return m, nil
				}
				m.switchConfirm = &switchConfirmState{
					app:            row.app,
					provider:       *row.provider,
					restoreSession: sessionRestoreEnabled(row.app),
					preview:        preview,
				}
				m.mode = modeSwitchConfirm
				return m, nil
			}
		}
	}

	return m, nil
}

func (m *Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.KeyMsg:
		switch typed.String() {
		case "esc":
			m.mode = modeList
			m.form = formState{}
			m.setStatus("已取消编辑", statusInfo)
			return m, nil
		case "tab", "down":
			m.form.focusIndex = (m.form.focusIndex + 1) % len(m.form.fields)
			m.syncFormFocus()
			return m, nil
		case "shift+tab", "up":
			m.form.focusIndex--
			if m.form.focusIndex < 0 {
				m.form.focusIndex = len(m.form.fields) - 1
			}
			m.syncFormFocus()
			return m, nil
		case "enter":
			if m.form.focusIndex >= len(m.form.fields)-1 {
				return m.saveForm()
			}
			m.form.focusIndex = (m.form.focusIndex + 1) % len(m.form.fields)
			m.syncFormFocus()
			return m, nil
		case "ctrl+s":
			return m.saveForm()
		}
	}

	var cmds []tea.Cmd
	for index := range m.form.fields {
		field, cmd := m.form.fields[index].Update(msg)
		m.form.fields[index] = field
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.KeyMsg:
		switch typed.String() {
		case "q", "n":
			m.mode = modeList
			m.confirm = nil
			m.setStatus("已取消删除", statusInfo)
			return m, nil
		case "enter", "y":
			if m.confirm == nil {
				m.mode = modeList
				return m, nil
			}
			if m.current[m.confirm.app] == m.confirm.provider.ID && !m.canDeleteCurrentConfirm() {
				return m, nil
			}
			if err := m.store.DeleteProvider(m.confirm.app, m.confirm.provider.ID); err != nil {
				m.mode = modeList
				m.confirm = nil
				m.setStatus(err.Error(), statusError)
				return m, nil
			}
			deletedName := m.confirm.provider.Name
			m.selectedKey = ""
			m.mode = modeList
			m.confirm = nil
			if err := m.reload(); err != nil {
				m.setStatus(err.Error(), statusError)
				return m, nil
			}
			m.setStatus(fmt.Sprintf("已删除 %s", deletedName), statusSuccess)
		}
	}

	return m, nil
}

func (m *Model) updateSwitchConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.switchConfirm == nil {
		m.mode = modeList
		return m, nil
	}

	typed, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch typed.String() {
	case "q", "esc":
		m.mode = modeList
		m.switchConfirm = nil
		m.setStatus("已取消切换", statusInfo)
		return m, nil
	case "up", "k":
		if m.switchConfirm.diffOffset > 0 {
			m.switchConfirm.diffOffset--
		}
		return m, nil
	case "down", "j":
		m.switchConfirm.diffOffset++
		return m, nil
	case "pgup", "ctrl+u":
		m.switchConfirm.diffOffset -= 8
		if m.switchConfirm.diffOffset < 0 {
			m.switchConfirm.diffOffset = 0
		}
		return m, nil
	case "pgdown", "ctrl+d":
		m.switchConfirm.diffOffset += 8
		return m, nil
	case "home":
		m.switchConfirm.diffOffset = 0
		return m, nil
	case "r", "space", " ":
		if sessionRestoreEnabled(m.switchConfirm.app) {
			m.switchConfirm.restoreSession = !m.switchConfirm.restoreSession
		}
		return m, nil
	case "n":
		if !sessionRestoreEnabled(m.switchConfirm.app) {
			m.mode = modeList
			m.switchConfirm = nil
			m.setStatus("已取消切换", statusInfo)
			return m, nil
		}
		state := *m.switchConfirm
		m.mode = modeList
		m.switchConfirm = nil
		return m.switchProvider(state.app, state.provider, false)
	case "enter", "y":
		state := *m.switchConfirm
		m.mode = modeList
		m.switchConfirm = nil
		return m.switchProvider(state.app, state.provider, state.restoreSession)
	default:
		return m, nil
	}
}

func (m *Model) switchProvider(app ccswitch.AppType, provider ccswitch.Provider, restoreSession bool) (tea.Model, tea.Cmd) {
	if err := m.store.SwitchProvider(app, provider.ID); err != nil {
		m.setStatus(err.Error(), statusError)
		return m, nil
	}

	m.selectedKey = providerKey(app, provider.ID)
	if err := m.reload(); err != nil {
		m.setStatus(err.Error(), statusError)
		return m, nil
	}

	status := fmt.Sprintf("已切换 %s -> %s", app.DisplayName(), provider.Name)
	statusKind := statusSuccess
	if restoreSession && app == ccswitch.AppCodex {
		workspace, err := codex.OpenWorkspace(m.store.CodexConfigDir(), false)
		if err != nil {
			status += fmt.Sprintf("；会话恢复失败: %v", err)
			statusKind = statusError
		} else {
			report, err := workspace.SwitchToCurrentProvider(codex.SwitchProviderOptions{})
			if err != nil {
				status += fmt.Sprintf("；会话恢复失败: %v", err)
				statusKind = statusError
			} else {
				status += formatSessionRestoreStatus(report)
			}
		}
	} else if restoreSession && app == ccswitch.AppGrok {
		input := m.store.ExtractInput(app, provider)
		report, err := grok.SwitchToModel(m.store.ConfigDir(ccswitch.AppGrok), input.Model)
		if err != nil {
			status += fmt.Sprintf("；会话恢复失败: %v", err)
			statusKind = statusError
		} else if report.Updated > 0 {
			status += fmt.Sprintf("；已恢复 %d 个会话", report.Updated)
		} else {
			status += "；会话无需恢复"
		}
	} else if restoreSession && app == ccswitch.AppPi {
		input := m.store.ExtractInput(app, provider)
		report, err := pi.SwitchToModel(m.store.PiSessionDir(), provider.ID, input.Model)
		if err != nil {
			status += fmt.Sprintf("；会话恢复失败: %v", err)
			statusKind = statusError
		} else if report.Updated > 0 {
			status += fmt.Sprintf("；已恢复 %d 个会话", report.Updated)
		} else {
			status += "；会话无需恢复"
		}
	}

	m.setStatus(status, statusKind)
	return m, nil
}

func formatSessionRestoreStatus(report *codex.RepairReport) string {
	if report == nil || len(report.Threads) == 0 {
		if report != nil && len(report.SkippedThreads) > 0 {
			return fmt.Sprintf("；会话无需恢复，跳过 %d 个", len(report.SkippedThreads))
		}
		return "；会话无需恢复"
	}
	status := fmt.Sprintf("；已恢复 %d 个会话", len(report.Threads))
	if len(report.SkippedThreads) > 0 {
		status += fmt.Sprintf("，跳过 %d 个", len(report.SkippedThreads))
	}
	return status
}

func (m *Model) openAddForm(app ccswitch.AppType) {
	m.mode = modeForm
	m.form = newFormState(app, nil, ccswitch.ProviderInput{})
}

func (m *Model) openEditForm(app ccswitch.AppType, provider ccswitch.Provider) {
	m.mode = modeForm
	m.form = newFormState(app, &provider, m.store.ExtractInput(app, provider))
}

func (m *Model) saveForm() (tea.Model, tea.Cmd) {
	input := m.formInput()
	if strings.TrimSpace(input.Name) == "" {
		m.form.errorMessage = "Name is required"
		return m, nil
	}

	var statusMessage string

	if m.form.editMode && m.form.original != nil {
		updated, err := m.store.UpdateProvider(m.form.app, *m.form.original, input)
		if err != nil {
			m.form.errorMessage = err.Error()
			return m, nil
		}
		m.selectedKey = providerKey(m.form.app, updated.ID)
		statusMessage = fmt.Sprintf("已更新 %s", updated.Name)
	} else {
		created, autoSwitched, err := m.store.AddProvider(m.form.app, input)
		if err != nil {
			m.form.errorMessage = err.Error()
			return m, nil
		}
		m.selectedKey = providerKey(m.form.app, created.ID)
		statusMessage = fmt.Sprintf("已添加 %s", created.Name)
		if autoSwitched {
			statusMessage += "，并自动设为当前供应商"
		}
	}

	m.mode = modeList
	m.form = formState{}
	if err := m.reload(); err != nil {
		m.setStatus(err.Error(), statusError)
		return m, nil
	}
	m.setStatus(statusMessage, statusSuccess)
	return m, nil
}

func (m *Model) reload() error {
	snapshot, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	m.providers = snapshot.Providers
	m.current = snapshot.Current
	m.rebuildRows()
	return nil
}

func (m *Model) rebuildRows() {
	rows := make([]listRow, 0, 32)
	for index, app := range ccswitch.AllAppTypes {
		if index > 0 {
			rows = append(rows, listRow{kind: rowSpacer, key: fmt.Sprintf("spacer:%d", index)})
		}
		rows = append(rows, listRow{
			kind: rowHeading,
			app:  app,
			key:  headingKey(app),
		})
		for index := range m.providers[app] {
			provider := m.providers[app][index]
			copyProvider := provider
			rows = append(rows, listRow{
				kind:     rowProvider,
				app:      app,
				provider: &copyProvider,
				key:      providerKey(app, provider.ID),
			})
		}
		rows = append(rows, listRow{
			kind: rowAdd,
			app:  app,
			key:  addKey(app),
		})
	}

	m.rows = rows
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}

	if m.selectedKey != "" {
		for index, row := range m.rows {
			if row.key == m.selectedKey {
				m.cursor = index
				return
			}
		}
	}

	for index, row := range m.rows {
		if isSelectableRow(row) {
			m.cursor = index
			return
		}
	}
	m.cursor = 0
}

func (m *Model) moveCursor(delta int) {
	if len(m.rows) == 0 {
		return
	}
	next := m.cursor
	for {
		next += delta
		if next < 0 || next >= len(m.rows) {
			return
		}
		if isSelectableRow(m.rows[next]) {
			m.cursor = next
			m.selectedKey = m.rows[next].key
			return
		}
	}
}

func (m *Model) moveToEdge(top bool) {
	if len(m.rows) == 0 {
		return
	}

	if top {
		for index, row := range m.rows {
			if isSelectableRow(row) {
				m.cursor = index
				m.selectedKey = row.key
				return
			}
		}
		return
	}

	for index := len(m.rows) - 1; index >= 0; index-- {
		if isSelectableRow(m.rows[index]) {
			m.cursor = index
			m.selectedKey = m.rows[index].key
			return
		}
	}
}

func (m *Model) jumpToApp(app ccswitch.AppType) {
	for index, row := range m.rows {
		if row.app == app && isSelectableRow(row) {
			m.cursor = index
			m.selectedKey = row.key
			return
		}
	}
}

func (m *Model) selectedRow() *listRow {
	if len(m.rows) == 0 || m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	if !isSelectableRow(m.rows[m.cursor]) {
		return nil
	}
	return &m.rows[m.cursor]
}

func (m *Model) setStatus(message string, kind statusLevel) {
	m.status = message
	m.statusKind = kind
}

func renderDiffLine(line string, width int) string {
	line = truncate(line, width)
	switch {
	case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
		return diffHeaderStyle.Render(line)
	case strings.HasPrefix(line, "+ "):
		return diffAddStyle.Render(line)
	case strings.HasPrefix(line, "- "):
		return diffRemoveStyle.Render(line)
	default:
		return diffContextStyle.Render(line)
	}
}

func (m *Model) viewList() string {
	helpLines := m.renderHelpLines()
	statusLines := strings.Split(m.renderStatusLine(), "\n")
	bodyHeight := m.height - 2 - len(statusLines) - len(helpLines)
	if bodyHeight < 6 {
		bodyHeight = 6
	}

	start := 0
	if len(m.rows) > bodyHeight {
		start = m.cursor - bodyHeight/2
		if start < 0 {
			start = 0
		}
		if start > len(m.rows)-bodyHeight {
			start = len(m.rows) - bodyHeight
		}
	}

	end := start + bodyHeight
	if end > len(m.rows) {
		end = len(m.rows)
	}

	lines := []string{m.renderHeader(), ""}
	for index := start; index < end; index++ {
		row := m.rows[index]
		switch row.kind {
		case rowHeading:
			lines = append(lines, m.renderGroupHeading(row.app))
		case rowSpacer:
			lines = append(lines, "")
		case rowProvider:
			lines = append(lines, m.renderProviderRow(index, row))
		case rowAdd:
			lines = append(lines, m.renderAddRow(index, row))
		}
	}
	for len(lines) < 2+bodyHeight {
		lines = append(lines, "")
	}
	lines = append(lines, statusLines...)
	lines = append(lines, helpLines...)
	return strings.Join(lines, "\n")
}

func (m *Model) viewForm() string {
	title := "Add " + m.form.app.DisplayName() + " Provider"
	if m.form.editMode {
		title = "Edit " + m.form.app.DisplayName() + " Provider"
	}

	lines := []string{
		panelTitleStyle.Render(title),
		formHintStyle.Render(m.formHint()),
		"",
	}
	for index, field := range m.form.fields {
		lines = append(lines, labelStyle.Render(m.form.labels[index]))
		lines = append(lines, field.View())
		lines = append(lines, "")
	}
	if m.form.errorMessage != "" {
		lines = append(lines, errorStyle.Render(m.form.errorMessage))
	}
	panelLines := strings.Split(panelStyle.Width(max(60, min(m.width-4, 100))).Render(strings.Join(lines, "\n")), "\n")
	helpLines := m.renderHelpLines()
	page := []string{m.renderHeader(), ""}
	page = append(page, panelLines...)
	for len(page)+len(helpLines) < m.height {
		page = append(page, "")
	}
	page = append(page, helpLines...)
	return strings.Join(page, "\n")
}

func (m *Model) viewConfirm() string {
	if m.confirm == nil {
		return ""
	}

	title := "Delete Provider"
	canDeleteCurrent := m.canDeleteCurrentConfirm()
	body := []string{
		dangerStyle.Render(title),
		"",
		fmt.Sprintf("App: %s", m.confirm.app.DisplayName()),
		fmt.Sprintf("Provider: %s", m.confirm.provider.Name),
		"",
		"The current provider cannot be deleted.",
		"Switch to another provider first.",
	}

	if m.current[m.confirm.app] != m.confirm.provider.ID {
		body = []string{
			dangerStyle.Render(title),
			"",
			fmt.Sprintf("App: %s", m.confirm.app.DisplayName()),
			fmt.Sprintf("Provider: %s", m.confirm.provider.Name),
		}
		body = append(body, providerURLLines(m.store, m.confirm.app, m.confirm.provider, max(24, min(m.width-8, 80)-6))...)
		body = append(body,
			"",
			"This will remove the provider record from the database.",
			"Press Enter / y to confirm, q / n to go back.",
		)
	} else if canDeleteCurrent {
		body = []string{
			dangerStyle.Render(title),
			"",
			fmt.Sprintf("App: %s", m.confirm.app.DisplayName()),
			fmt.Sprintf("Provider: %s", m.confirm.provider.Name),
			"",
			"This is the last provider for the app.",
			"After deletion, the app will have no active provider.",
			"Press Enter / y to confirm, q / n to go back.",
		}
	}

	panelLines := strings.Split(panelStyle.Width(max(50, min(m.width-8, 80))).Render(strings.Join(body, "\n")), "\n")
	helpLines := m.renderHelpLines()
	page := []string{m.renderHeader()}
	topPadding := (m.height - len(panelLines) - len(helpLines) - 1) / 2
	if topPadding < 1 {
		topPadding = 1
	}
	for i := 0; i < topPadding; i++ {
		page = append(page, "")
	}
	page = append(page, panelLines...)
	for len(page)+len(helpLines) < m.height {
		page = append(page, "")
	}
	page = append(page, helpLines...)
	return strings.Join(page, "\n")
}

func (m *Model) viewSwitchConfirm() string {
	if m.switchConfirm == nil {
		return ""
	}

	helpLines := m.renderHelpLines()
	preview := m.switchConfirm.preview
	currentName := "未知"
	if preview != nil {
		currentName = preview.CurrentProvider
	}

	restore := "关闭"
	if m.switchConfirm.restoreSession {
		restore = "开启"
	}
	body := []string{
		panelTitleStyle.Render("Switch Provider"),
		"",
		fmt.Sprintf("App: %s", m.switchConfirm.app.DisplayName()),
		fmt.Sprintf("Current: %s", currentName),
		fmt.Sprintf("Provider: %s", m.switchConfirm.provider.Name),
		"",
	}
	body = append(body, providerURLLines(m.store, m.switchConfirm.app, m.switchConfirm.provider, max(24, min(m.width-8, 80)-6))...)

	if preview != nil {
		body = append(body, "", "Files:")
		for _, file := range preview.Files {
			if file.Changed() {
				body = append(body, fmt.Sprintf("  %s (+%d -%d)", file.Path, file.Added, file.Removed))
			} else {
				body = append(body, fmt.Sprintf("  %s (no changes)", file.Path))
			}
		}

		diffLines := preview.DiffLines()
		body = append(body, "", "Diff:")
		visibleLines := max(4, m.height-len(helpLines)-len(body)-8)
		if m.switchConfirm.diffOffset > len(diffLines) {
			m.switchConfirm.diffOffset = len(diffLines)
		}
		start := m.switchConfirm.diffOffset
		end := start + visibleLines
		if end > len(diffLines) {
			end = len(diffLines)
		}
		if start > 0 {
			body = append(body, mutedStyle.Render("... 上方还有 Diff ..."))
		}
		for _, line := range diffLines[start:end] {
			body = append(body, renderDiffLine(line, max(24, min(m.width-8, 100)-6)))
		}
		if end < len(diffLines) {
			body = append(body, mutedStyle.Render("... 下方还有 Diff ..."))
		}
	}

	if sessionRestoreEnabled(m.switchConfirm.app) {
		body = append(body,
			"",
			fmt.Sprintf("%s 会话自动恢复: %s", m.switchConfirm.app.DisplayName(), restore),
			"切换后会将历史会话切换到当前模型。",
		)
	} else {
		body = append(body, "", "确认后才会写入 live 配置。")
	}

	panelLines := strings.Split(panelStyle.Width(max(50, min(m.width-8, 80))).Render(strings.Join(body, "\n")), "\n")
	page := []string{m.renderHeader()}
	topPadding := (m.height - len(panelLines) - len(helpLines) - 1) / 2
	if topPadding < 1 {
		topPadding = 1
	}
	for i := 0; i < topPadding; i++ {
		page = append(page, "")
	}
	page = append(page, panelLines...)
	for len(page)+len(helpLines) < m.height {
		page = append(page, "")
	}
	page = append(page, helpLines...)
	return strings.Join(page, "\n")
}

func (m *Model) renderHeader() string {
	left := titleStyle.Render("CC Switch TUI") + " " + badgeStyle.Render(m.modeLabel())
	if m.mode == modeList || m.mode == modeForm {
		return left
	}
	totalWidth := max(40, m.width)
	rightText := m.renderHeaderMeta(max(0, totalWidth-lipgloss.Width(left)-1))
	if rightText == "" {
		return left
	}

	right := headerMetaStyle.Render(rightText)
	gap := totalWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) renderHeaderMeta(maxWidth int) string {
	if maxWidth < 8 {
		return ""
	}

	full := make([]string, 0, len(ccswitch.AllAppTypes))
	compact := make([]string, 0, len(ccswitch.AllAppTypes))
	minimal := make([]string, 0, len(ccswitch.AllAppTypes))

	for _, app := range ccswitch.AllAppTypes {
		shortApp := string([]rune(app.DisplayName())[0])
		full = append(full, fmt.Sprintf("%s:%d", app.DisplayName(), len(m.providers[app])))
		compact = append(compact, fmt.Sprintf("%s:%d", shortApp, len(m.providers[app])))
		minimal = append(minimal, fmt.Sprintf("%s:%d", shortApp, len(m.providers[app])))
	}

	candidates := []string{
		strings.Join(full, " | "),
		strings.Join(compact, " | "),
		strings.Join(minimal, " | "),
	}

	for _, candidate := range candidates {
		if lipgloss.Width(candidate) <= maxWidth {
			return candidate
		}
	}

	return truncate(strings.Join(minimal, " | "), maxWidth)
}

func (m *Model) renderGroupHeading(app ccswitch.AppType) string {
	label := groupStyle.Render(app.DisplayName())
	if app.IsIncremental() {
		label += mutedStyle.Render(" (I)")
	}
	summary := mutedStyle.Render(fmt.Sprintf("%d 个供应商", len(m.providers[app])))
	return label + " " + summary
}

func (m *Model) renderProviderRow(index int, row listRow) string {
	if row.provider == nil {
		return ""
	}

	selected := index == m.cursor
	isCurrent := !row.app.IsIncremental() && m.current[row.app] == row.provider.ID
	prefix := "  "
	if selected {
		prefix = "▶ "
	}
	currentMark := " "
	if isCurrent {
		currentMark = "●"
	}

	nameWidth, endpointWidth := m.providerColumnWidths(isCurrent)
	name := padRight(truncate(row.provider.Name, nameWidth), nameWidth)
	endpoint := padRight(truncate(m.store.EndpointSummary(row.app, *row.provider), endpointWidth), endpointWidth)

	line := strings.TrimRight(fmt.Sprintf("%s%s %s %s", prefix, currentMark, name, endpoint), " ")
	if selected {
		return selectedStyle.Render(line)
	}
	if isCurrent {
		return currentRowStyle.Render(line)
	}
	return line
}

func (m *Model) renderAddRow(index int, row listRow) string {
	selected := index == m.cursor
	prefix := "  "
	if selected {
		prefix = "▶ "
	}
	line := truncate(fmt.Sprintf("%s+ 添加 %s 供应商", prefix, row.app.DisplayName()), max(20, m.width-2))
	if selected {
		return selectedAddStyle.Render(line)
	}
	return addRowStyle.Render(line)
}

func (m *Model) renderStatusLine() string {
	text := m.status
	if text == "" {
		text = "就绪"
	}
	prefix := "信息"
	switch m.statusKind {
	case statusError:
		prefix = "错误"
	case statusSuccess:
		prefix = "完成"
	}
	segments := []string{prefix + " · " + strings.ReplaceAll(text, "\n", " ")}
	segments = append(segments, m.selectedProviderStatusSegments()...)
	content := strings.Join(segments, " | ")
	contentWidth := max(16, m.width-4)
	return statusBarStyle.Width(max(24, m.width)).Render(strings.Join(wrapText(content, contentWidth), "\n"))
}

func (m *Model) selectedProviderStatusSegments() []string {
	row := m.selectedRow()
	if row == nil || row.kind != rowProvider || row.provider == nil {
		return nil
	}

	input := m.store.ExtractInput(row.app, *row.provider)
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		baseURL = providerBaseURLFallback(row.app)
	}

	segments := []string{"Base URL: " + baseURL}
	if website := strings.TrimSpace(input.Website); website != "" {
		segments = append(segments, "Website: "+website)
	}
	return segments
}

func (m *Model) renderHelpLines() []string {
	var items []string
	switch m.mode {
	case modeForm:
		items = []string{
			help("Enter", "下一项/保存"),
			help("Tab", "下一项"),
			help("Shift+Tab", "上一项"),
			help("Ctrl+S", "保存"),
			help("Esc", "返回"),
		}
	case modeConfirm:
		if m.confirm != nil && m.current[m.confirm.app] == m.confirm.provider.ID && !m.canDeleteCurrentConfirm() {
			items = []string{help("q", "返回")}
		} else {
			items = []string{
				help("Enter/y", "确认"),
				help("q/n", "返回"),
			}
		}
	case modeSwitchConfirm:
		items = []string{help("Enter/y", "确认切换"), help("↑/↓ j/k", "滚动 Diff"), help("PgUp/PgDn", "翻页")}
		if m.switchConfirm != nil && sessionRestoreEnabled(m.switchConfirm.app) {
			items = append(items, help("r/Space", "开关会话恢复"), help("n", "切换但不恢复"))
		}
		items = append(items, help("q/Esc", "返回"))
	default:
		items = []string{
			help("↑/↓ j/k", "移动"),
			help("Enter", "设为当前"),
			help("a", "添加"),
			help("e", "编辑"),
			help("d", "删除"),
			help("1/2/3/4/5/6", "跳应用"),
			help("g/G", "顶/底"),
			help("q", "退出"),
		}
	}
	return wrapInlineItems(items, max(20, m.width-2))
}

func sessionRestoreEnabled(app ccswitch.AppType) bool {
	return app == ccswitch.AppCodex || app == ccswitch.AppGrok || app == ccswitch.AppPi
}

func newFormState(app ccswitch.AppType, provider *ccswitch.Provider, input ccswitch.ProviderInput) formState {
	labels := []string{
		"Name",
		"Base URL",
		"API Key",
		"Model",
	}
	values := []string{
		input.Name,
		input.BaseURL,
		input.APIKey,
		input.Model,
	}

	if app == ccswitch.AppCodex {
		labels = append(labels, "Reasoning Effort")
		values = append(values, input.ReasoningEffort)
	}

	labels = append(labels, "Website", "Notes")
	values = append(values, input.Website, input.Notes)

	fields := make([]textinput.Model, 0, len(labels))
	for index, label := range labels {
		field := textinput.New()
		field.SetValue(values[index])
		field.Prompt = "› "
		field.CharLimit = 2048
		field.Width = 72
		field.Placeholder = placeholderFor(app, label)
		if label == "API Key" {
			field.EchoMode = textinput.EchoPassword
			field.EchoCharacter = '*'
		}
		fields = append(fields, field)
	}

	state := formState{
		app:        app,
		editMode:   provider != nil,
		original:   provider,
		fields:     fields,
		labels:     labels,
		focusIndex: 0,
	}
	state.syncFocus()
	return state
}

func (m *Model) syncFormFocus() {
	m.form.syncFocus()
}

func (f *formState) syncFocus() {
	for index := range f.fields {
		if index == f.focusIndex {
			f.fields[index].Focus()
			f.fields[index].PromptStyle = currentStyle
			f.fields[index].TextStyle = lipgloss.NewStyle().Bold(true)
			continue
		}
		f.fields[index].Blur()
		f.fields[index].PromptStyle = lipgloss.NewStyle()
		f.fields[index].TextStyle = lipgloss.NewStyle()
	}
}

func (m *Model) formInput() ccswitch.ProviderInput {
	field := func(index int) string {
		if index >= 0 && index < len(m.form.fields) {
			return strings.TrimSpace(m.form.fields[index].Value())
		}
		return ""
	}

	input := ccswitch.ProviderInput{
		Name:    field(0),
		BaseURL: field(1),
		APIKey:  field(2),
		Model:   field(3),
	}

	next := 4
	if m.form.app == ccswitch.AppCodex {
		input.ReasoningEffort = field(next)
		next++
	}
	input.Website = field(next)
	input.Notes = field(next + 1)
	return input
}

func help(key, desc string) string {
	return helpKeyStyle.Render("["+key+"]") + desc
}

func isSelectableRow(row listRow) bool {
	return row.kind == rowProvider || row.kind == rowAdd
}

func headingKey(app ccswitch.AppType) string {
	return "heading:" + app.String()
}

func providerKey(app ccswitch.AppType, id string) string {
	return "provider:" + app.String() + ":" + id
}

func addKey(app ccswitch.AppType) string {
	return "add:" + app.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *Model) modeLabel() string {
	switch m.mode {
	case modeForm:
		if m.form.editMode {
			return "编辑"
		}
		return "新增"
	case modeConfirm:
		return "确认"
	case modeSwitchConfirm:
		return "切换"
	default:
		return "列表"
	}
}

func (m *Model) currentProviderName(app ccswitch.AppType) string {
	currentID := m.current[app]
	if currentID == "" {
		return ""
	}
	for _, provider := range m.providers[app] {
		if provider.ID == currentID {
			return provider.Name
		}
	}
	return currentID
}

func (m *Model) canDeleteCurrentConfirm() bool {
	if m.confirm == nil {
		return false
	}
	if m.current[m.confirm.app] != m.confirm.provider.ID {
		return true
	}
	return m.providerCount(m.confirm.app) == 1
}

func (m *Model) providerCount(app ccswitch.AppType) int {
	return len(m.providers[app])
}

func (m *Model) formHint() string {
	paths := m.store.ConfigPaths(m.form.app)
	if len(paths) > 0 {
		return "Saved to " + strings.Join(paths, " and ")
	}
	return "Saved to the app live config"
}

func placeholderFor(app ccswitch.AppType, label string) string {
	switch label {
	case "Name":
		return "e.g. Official / Relay / Company Internal"
	case "Base URL":
		switch app {
		case ccswitch.AppClaude:
			return "e.g. https://api.anthropic.com"
		case ccswitch.AppCodex:
			return "e.g. https://api.openai.com/v1"
		case ccswitch.AppGemini:
			return "e.g. https://generativelanguage.googleapis.com"
		case ccswitch.AppGrok:
			return "e.g. https://api.x.ai/v1"
		}
	case "API Key":
		return "Leave empty to keep OAuth / login semantics"
	case "Model":
		switch app {
		case ccswitch.AppClaude:
			return "e.g. claude-sonnet-4-5"
		case ccswitch.AppCodex:
			return "e.g. gpt-5-codex"
		case ccswitch.AppGemini:
			return "e.g. gemini-2.5-pro"
		case ccswitch.AppGrok:
			return "e.g. grok-4.6"
		case ccswitch.AppPi:
			return "e.g. gpt-4o"
		}
	case "Reasoning Effort":
		return "e.g. medium / high"
	case "Website":
		return "Optional: provider website"
	case "Notes":
		return "Optional: notes"
	}
	return label
}

func providerURLLines(store *ccswitch.Store, app ccswitch.AppType, provider ccswitch.Provider, width int) []string {
	input := store.ExtractInput(app, provider)
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		baseURL = providerBaseURLFallback(app)
	}

	lines := wrapLabelValue("Base URL", baseURL, width)
	if website := strings.TrimSpace(input.Website); website != "" {
		lines = append(lines, wrapLabelValue("Website", website, width)...)
	}

	for index := range lines {
		lines[index] = mutedStyle.Render(lines[index])
	}
	return lines
}

func providerBaseURLFallback(app ccswitch.AppType) string {
	switch app {
	case ccswitch.AppClaude, ccswitch.AppCodex:
		return "官方登录"
	case ccswitch.AppGemini:
		return "Google OAuth"
	case ccswitch.AppGrok:
		return "官方登录"
	case ccswitch.AppPi:
		return "官方登录"
	default:
		return "-"
	}
}

func (m *Model) providerColumnWidths(isCurrent bool) (int, int) {
	total := max(36, m.width-6)
	statusWidth := 0
	if isCurrent {
		statusWidth = lipgloss.Width("当前") + 1
	}
	contentWidth := total - 4 - statusWidth
	if contentWidth < 20 {
		contentWidth = 20
	}
	nameWidth := contentWidth * 2 / 5
	if nameWidth < 12 {
		nameWidth = 12
	}
	if nameWidth > 28 {
		nameWidth = 28
	}
	endpointWidth := contentWidth - nameWidth - 1
	if endpointWidth < 12 {
		endpointWidth = 12
	}
	return nameWidth, endpointWidth
}

func wrapLabelValue(label, value string, width int) []string {
	prefix := label + ": "
	if width <= 0 {
		return nil
	}

	prefixWidth := lipgloss.Width(prefix)
	available := width - prefixWidth
	if available < 8 {
		lines := []string{truncate(prefix, width)}
		for _, line := range wrapText(value, max(4, width-2)) {
			lines = append(lines, "  "+line)
		}
		return lines
	}

	wrapped := wrapText(value, available)
	if len(wrapped) == 0 {
		return []string{prefix}
	}

	lines := []string{prefix + wrapped[0]}
	indent := strings.Repeat(" ", prefixWidth)
	for _, line := range wrapped[1:] {
		lines = append(lines, indent+line)
	}
	return lines
}

func wrapText(input string, width int) []string {
	if width <= 0 {
		return nil
	}
	if input == "" {
		return []string{""}
	}

	lines := make([]string, 0, 1)
	var builder strings.Builder
	used := 0

	for _, r := range input {
		runeWidth := lipgloss.Width(string(r))
		if runeWidth > width {
			runeWidth = width
		}
		if used+runeWidth > width && builder.Len() > 0 {
			lines = append(lines, builder.String())
			builder.Reset()
			used = 0
		}
		builder.WriteRune(r)
		used += runeWidth
	}

	if builder.Len() > 0 {
		lines = append(lines, builder.String())
	}
	return lines
}

func wrapInlineItems(items []string, width int) []string {
	if len(items) == 0 {
		return nil
	}

	lines := make([]string, 0, 2)
	current := ""
	for _, item := range items {
		next := item
		if current != "" {
			next = current + " " + item
		}
		if current != "" && lipgloss.Width(next) > width {
			lines = append(lines, current)
			current = item
			continue
		}
		current = next
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func padRight(input string, width int) string {
	gap := width - lipgloss.Width(input)
	if gap <= 0 {
		return input
	}
	return input + strings.Repeat(" ", gap)
}

func truncate(input string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if lipgloss.Width(input) <= limit {
		return input
	}
	if limit <= 1 {
		return "…"
	}

	var builder strings.Builder
	used := 0
	for _, r := range input {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth+1 > limit {
			break
		}
		builder.WriteRune(r)
		used += runeWidth
	}

	if builder.Len() == 0 {
		return "…"
	}
	return builder.String() + "…"
}

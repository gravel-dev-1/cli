package components

import (
	"fmt"
	"io"

	"gravel/manifest"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type BaseMultiSelector struct {
	list     list.Model
	selected map[int]manifest.Base
}

type multiBaseItemDelegate struct {
	baseItemDelegate
	selector *BaseMultiSelector
}

func (mbd multiBaseItemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(baseItem)
	if !ok {
		return
	}

	char := "○"

	style := lipgloss.NewStyle().Foreground(lipgloss.Color(item.Color))
	fn := style.PaddingLeft(2).Render
	if index == m.Index() {
		fn = func(s ...string) string { return "> " + style.Render(s...) }
	}
	if _, ok := mbd.selector.selected[index]; ok {
		char = "●"
	}

	_, _ = fmt.Fprint(w, fn(char, item.Name))
}

func NewBaseMultiSelector(bases ...manifest.Base) *BaseMultiSelector {
	var items []list.Item
	for _, value := range bases {
		items = append(items, baseItem(value))
	}

	selector := &BaseMultiSelector{
		selected: make(map[int]manifest.Base),
	}

	l := list.New(items, multiBaseItemDelegate{selector: selector}, 0, 0)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	selector.list = l

	return selector
}

func (BaseMultiSelector) Init() tea.Cmd { return nil }

func (m *BaseMultiSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, (msg.Height/2)-2)
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeySpace:
			if selected, ok := m.list.SelectedItem().(baseItem); ok {
				if _, ok := m.selected[m.list.Index()]; ok {
					delete(m.selected, m.list.Index())
				} else {
					m.selected[m.list.Index()] = manifest.Base(selected)
				}
			}

		case tea.KeyEnter:
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m BaseMultiSelector) View() string { return m.list.View() }
func (m BaseMultiSelector) Selected() (bases []manifest.Base) {
	for _, base := range m.selected {
		bases = append(bases, base)
	}
	return
}

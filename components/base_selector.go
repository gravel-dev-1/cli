package components

import (
	"fmt"
	"io"

	"gravel/manifest"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type BaseSelector struct {
	list     list.Model
	selected *manifest.Base
}

type baseItem manifest.Base

func (i baseItem) FilterValue() string { return i.Remote.Name }
func (i baseItem) Title() string       { return i.Name }

type baseItemDelegate struct{}

func (baseItemDelegate) Height() int                         { return 1 }
func (baseItemDelegate) Spacing() int                        { return 0 }
func (baseItemDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (baseItemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(baseItem)
	if !ok {
		return
	}

	style := lipgloss.NewStyle().Foreground(lipgloss.Color(i.Color))
	fn := style.PaddingLeft(2).Render
	if index == m.Index() {
		fn = func(s ...string) string { return "> " + style.Render(s...) }
	}

	_, _ = fmt.Fprint(w, fn(i.Name))
}

func NewBaseSelector(bases ...manifest.Base) *BaseSelector {
	var items []list.Item
	for _, value := range bases {
		items = append(items, baseItem(value))
	}

	l := list.New(items, baseItemDelegate{}, 0, 0)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	return &BaseSelector{list: l}
}

func (BaseSelector) Init() tea.Cmd { return nil }

func (m *BaseSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyEnter:
			if selected, ok := m.list.SelectedItem().(baseItem); ok {
				value := manifest.Base(selected)
				m.selected = &value
				m.list.SetSize(0, 0)
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m BaseSelector) View() string             { return m.list.View() }
func (m BaseSelector) Selected() *manifest.Base { return m.selected }

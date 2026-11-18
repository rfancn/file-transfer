package cliui

import "testing"

func TestMultiSelectWindow(t *testing.T) {
	items := make([]MultiItem, 50)
	for i := range items {
		items[i] = MultiItem{Text: "x", Selectable: true}
	}
	m := &multiSelect{items: items, height: 10}

	m.cursor = 25
	m.ensureVisible()
	if m.offset != 16 {
		t.Fatalf("offset = %d, want 16", m.offset)
	}
	if got := len(m.lines()); got != 12 {
		t.Fatalf("lines = %d, want 12", got)
	}

	m.cursor = 2
	m.ensureVisible()
	if m.offset != 2 {
		t.Fatalf("offset = %d, want 2", m.offset)
	}

	m.cursor = 49
	m.ensureVisible()
	if m.offset != 40 {
		t.Fatalf("offset = %d, want 40", m.offset)
	}
}

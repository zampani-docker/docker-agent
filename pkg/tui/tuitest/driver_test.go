package tuitest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// echoModel is a tiny model used to unit-test the harness without pulling in
// the real TUI. It echoes typed runes into a buffer, clears on Enter, and
// renders the buffer prefixed with "echo: ". A delayed command lets us prove
// WaitFor polls asynchronously delivered frames.
type echoModel struct {
	buf     string
	delayed string
}

func (m *echoModel) Init() tea.Cmd { return nil }

type delayedMsg struct{ text string }

func (m *echoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.Code {
		case tea.KeyEnter:
			m.buf = ""
		case 'x':
			// Pressing x schedules a message that lands a bit later, so a
			// WaitFor must poll rather than read a single synchronous frame.
			return m, tea.Tick(20*time.Millisecond, func(time.Time) tea.Msg {
				return delayedMsg{text: "later"}
			})
		default:
			m.buf += msg.Text
		}
	case delayedMsg:
		m.delayed = msg.text
	}
	return m, nil
}

func (m *echoModel) View() tea.View {
	return tea.NewView("echo: " + m.buf + " " + m.delayed)
}

func TestDriver_TypeAndAssert(t *testing.T) {
	d := New(t, &echoModel{}, 80, 24)
	d.Type("hi").Assert(Contains("echo: hi"))
}

func TestDriver_EnterClearsBuffer(t *testing.T) {
	d := New(t, &echoModel{}, 80, 24)
	d.Type("hello").
		Assert(Contains("hello")).
		Enter().
		Assert(Absent("hello"))
}

func TestDriver_WaitForPollsAsyncFrames(t *testing.T) {
	d := New(t, &echoModel{}, 80, 24, WithTimeout(2*time.Second))
	// The delayed message only arrives via tea.Tick, so a synchronous Assert
	// would miss it — WaitFor must poll until the frame updates.
	d.Press('x').WaitFor(Contains("later"))
}

func TestMatchers(t *testing.T) {
	t.Parallel()

	const frame = "the quick brown fox"
	cases := []struct {
		name string
		m    Matcher
		want bool
	}{
		{"contains hit", Contains("quick"), true},
		{"contains miss", Contains("slow"), false},
		{"containsAll hit", ContainsAll("quick", "fox"), true},
		{"containsAll miss", ContainsAll("quick", "cat"), false},
		{"absent hit", Absent("cat"), true},
		{"absent miss", Absent("fox"), false},
		{"matches hit", Matches("br[ow]+n"), true},
		{"matches miss", Matches("^fox"), false},
		{"not inverts", Not(Contains("cat")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.m.match(frame); got != tc.want {
				t.Errorf("%s.match(%q) = %v, want %v", tc.m.describe(), frame, got, tc.want)
			}
		})
	}
}

func TestStripFrame(t *testing.T) {
	t.Parallel()

	// ANSI bold red around "hi", with trailing padding that must be trimmed.
	v := tea.NewView("\x1b[1;31mhi\x1b[0m   \nworld   ")
	got := stripFrame(v)
	want := "hi\nworld"
	if got != want {
		t.Errorf("stripFrame = %q, want %q", got, want)
	}
}

func TestAssertGolden_UpdateThenCompare(t *testing.T) {
	// Point the golden at this package's testdata dir (created on demand).
	orig := *updateGolden
	t.Cleanup(func() { *updateGolden = orig })

	name := "unit_golden"
	path := filepath.Join(goldenDir, name+".golden")
	t.Cleanup(func() { _ = os.Remove(path) })

	*updateGolden = true
	assertGolden(t, name, "frame-A")

	*updateGolden = false
	assertGolden(t, name, "frame-A") // matches → no failure
}

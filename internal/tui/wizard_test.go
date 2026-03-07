package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// sendKey simulates a key press through the wizard's Update method.
func sendKey(m tea.Model, key string) tea.Model {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return updated
}

// sendSpecialKey simulates a special key (enter, left, etc.).
func sendSpecialKey(m tea.Model, keyType tea.KeyType) tea.Model {
	updated, _ := m.Update(tea.KeyMsg{Type: keyType})
	return updated
}

// skipToPackagesStep navigates from stepName through profile (and workspace
// if present) to reach stepPackages. Returns the model at stepPackages.
func skipToPackagesStep(t *testing.T, m tea.Model) tea.Model {
	t.Helper()

	// Step: name — type "test-session" and press Enter.
	for _, ch := range "test-session" {
		m = sendKey(m, string(ch))
	}
	m = sendSpecialKey(m, tea.KeyEnter)

	wm := m.(WizardModel)
	if wm.step != stepProfile {
		t.Fatalf("expected stepProfile after name, got step %d", wm.step)
	}

	// Step: profile — press Enter to accept default.
	m = sendSpecialKey(m, tea.KeyEnter)
	wm = m.(WizardModel)

	// If workspace step is present (user has workspaces configured), skip it.
	if wm.step == stepWorkspace {
		m = sendSpecialKey(m, tea.KeyEnter) // select "(none)"
		wm = m.(WizardModel)
	}

	if wm.step != stepPackages {
		t.Fatalf("expected stepPackages, got step %d", wm.step)
	}

	return m
}

// TestWizardPackagesCapture verifies that packages typed in the wizard
// are captured in the WizardResult.
func TestWizardPackagesCapture(t *testing.T) {
	wiz := NewWizard("", "/tmp/test-dir")
	var m tea.Model = wiz

	m = skipToPackagesStep(t, m)

	// Step: packages — type "rustup" and press Enter.
	for _, ch := range "rustup" {
		m = sendKey(m, string(ch))
	}

	// Verify text input has the typed text before pressing Enter.
	wm := m.(WizardModel)
	if got := wm.textInput.Value(); got != "rustup" {
		t.Fatalf("textInput.Value() = %q before Enter, want %q", got, "rustup")
	}

	m = sendSpecialKey(m, tea.KeyEnter)
	wm = m.(WizardModel)

	// After Enter, should be at permissions step.
	if wm.step != stepPermissions {
		t.Fatalf("expected stepPermissions after packages, got step %d", wm.step)
	}

	// Check packages were captured in result.
	if wm.result.Packages != "rustup" {
		t.Fatalf("result.Packages = %q after packages step, want %q", wm.result.Packages, "rustup")
	}

	// Step: permissions — press Enter (skip).
	m = sendSpecialKey(m, tea.KeyEnter)
	wm = m.(WizardModel)
	if wm.step != stepPrompt {
		t.Fatalf("expected stepPrompt after permissions, got step %d", wm.step)
	}

	// Step: prompt — press Enter (skip).
	m = sendSpecialKey(m, tea.KeyEnter)
	wm = m.(WizardModel)
	if wm.step != stepReview {
		t.Fatalf("expected stepReview after prompt, got step %d", wm.step)
	}

	// Verify packages still set at review.
	if wm.result.Packages != "rustup" {
		t.Fatalf("result.Packages = %q at review, want %q", wm.result.Packages, "rustup")
	}

	// Step: review — press Enter to confirm.
	m = sendSpecialKey(m, tea.KeyEnter)
	wm = m.(WizardModel)
	if wm.step != stepDone {
		t.Fatalf("expected stepDone after review, got step %d", wm.step)
	}

	// Final check: Result() should have packages.
	res := wm.Result()
	if res.Packages != "rustup" {
		t.Fatalf("Result().Packages = %q, want %q", res.Packages, "rustup")
	}
	if res.Name != "test-session" {
		t.Fatalf("Result().Name = %q, want %q", res.Name, "test-session")
	}
	if res.Cancelled {
		t.Fatal("Result().Cancelled = true, want false")
	}
}

// TestWizardEmptyPackages verifies that skipping packages (just Enter) works.
func TestWizardEmptyPackages(t *testing.T) {
	wiz := NewWizard("", "/tmp/test-dir")
	var m tea.Model = wiz

	m = skipToPackagesStep(t, m)

	// Packages — press Enter without typing.
	m = sendSpecialKey(m, tea.KeyEnter)

	wm := m.(WizardModel)
	if wm.result.Packages != "" {
		t.Fatalf("result.Packages = %q, want empty", wm.result.Packages)
	}
}

// TestWizardCommaPackages verifies multiple comma-separated packages.
func TestWizardCommaPackages(t *testing.T) {
	wiz := NewWizard("", "/tmp/test-dir")
	var m tea.Model = wiz

	m = skipToPackagesStep(t, m)

	// Packages — type "rustup,bun,nodejs".
	for _, ch := range "rustup,bun,nodejs" {
		m = sendKey(m, string(ch))
	}
	m = sendSpecialKey(m, tea.KeyEnter)

	wm := m.(WizardModel)
	if wm.result.Packages != "rustup,bun,nodejs" {
		t.Fatalf("result.Packages = %q, want %q", wm.result.Packages, "rustup,bun,nodejs")
	}
}

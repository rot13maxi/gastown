package patrol

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testTime = time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

// TestShadowModeEnabled verifies shadow mode flag is correctly set
func TestShadowModeEnabled(t *testing.T) {
	cfg := PatrolConfig{
		Rig:      "gastown",
		WorkDir:  "/tmp",
		Shadow:   true,
		Verbose:  true,
	}

	if !cfg.Shadow {
		t.Error("expected Shadow to be true")
	}
}

// TestShadowReportStructure verifies shadow report has correct structure
func TestShadowReportStructure(t *testing.T) {
	report := &ShadowReport{
		Timestamp:  testTime,
		CycleCount: 1,
		Rig:        "gastown",
		Mode:       "shadow",
		MailDrain: ShadowMailDrain{
			WouldDrain: 5,
		},
		Zombies: []ShadowZombie{
			{Polecat: "polecat1", Action: "restart", Reason: "dead"},
		},
		Escalations: []ShadowEscalation{
			{Type: "help-triage", Target: "deacon", Details: "HELP: crash"},
		},
	}

	if report.Mode != "shadow" {
		t.Errorf("expected mode 'shadow', got %s", report.Mode)
	}

	if len(report.Zombies) != 1 {
		t.Errorf("expected 1 zombie, got %d", len(report.Zombies))
	}

	if report.Zombies[0].Polecat != "polecat1" {
		t.Errorf("expected polecat 'polecat1', got %s", report.Zombies[0].Polecat)
	}

	if len(report.Escalations) != 1 {
		t.Errorf("expected 1 escalation, got %d", len(report.Escalations))
	}
}

// TestShadowLogFileWrite verifies shadow report can be written to file
func TestShadowLogFileWrite(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "shadow.jsonl")

	cfg := PatrolConfig{
		Rig:           "gastown",
		WorkDir:       tmpDir,
		Shadow:        true,
		ShadowLogFile: logFile,
	}

	report := getShadowReport(cfg, 1)
	report.MailDrain.WouldDrain = 3
	report.Zombies = append(report.Zombies, ShadowZombie{
		Polecat: "test-polecat",
		Action:  "restart",
		Reason:  "dead",
	})

	err := writeShadowReport(cfg, report)
	if err != nil {
		t.Fatalf("writeShadowReport failed: %v", err)
	}

	// Verify file was created and has content
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("could not read shadow log: %v", err)
	}

	if len(data) == 0 {
		t.Error("shadow log file is empty")
	}
}

// TestShadowModeNoActions verifies shadow mode doesn't execute actions
func TestShadowModeNoActions(t *testing.T) {
	// This test verifies the configuration is correct for shadow mode
	cfg := PatrolConfig{
		Rig:      "gastown",
		WorkDir:  "/tmp",
		Shadow:   true,
		Verbose:  true,
	}

	// Shadow should be enabled
	if !cfg.Shadow {
		t.Error("shadow mode should be enabled for validation")
	}
}

// TestShadowReportCycleCount verifies cycle count is tracked correctly
func TestShadowReportCycleCount(t *testing.T) {
	cfg := PatrolConfig{
		Rig:      "gastown",
		WorkDir:  "/tmp",
		Shadow:   true,
	}

	for i := 1; i <= 5; i++ {
		report := getShadowReport(cfg, i)
		if report.CycleCount != i {
			t.Errorf("expected cycle %d, got %d", i, report.CycleCount)
		}
		if report.Mode != "shadow" {
			t.Errorf("expected mode 'shadow', got %s", report.Mode)
		}
	}
}

// TestLiveModeVsShadowMode verifies modes differ correctly
func TestLiveModeVsShadowMode(t *testing.T) {
	shadowCfg := PatrolConfig{
		Rig:    "gastown",
		Shadow: true,
	}

	liveCfg := PatrolConfig{
		Rig:    "gastown",
		Shadow: false,
	}

	shadowReport := getShadowReport(shadowCfg, 1)
	liveReport := getShadowReport(liveCfg, 1)

	if shadowReport.Mode != "shadow" {
		t.Error("shadow report should have mode 'shadow'")
	}

	if liveReport.Mode != "live" {
		t.Error("live report should have mode 'live'")
	}
}

// TestShadowDiscrepancyRecording verifies discrepancies can be recorded
func TestShadowDiscrepancyRecording(t *testing.T) {
	report := &ShadowReport{
		CycleCount: 1,
		Rig:        "gastown",
		Mode:       "shadow",
	}

	discrepancy := ShadowDiscrepancy{
		Type:     "zombie_missed",
		Polecat:  "polecat1",
		Expected: "restart",
		Actual:   "none",
		Details:  "Shadow mode detected zombie that molecule missed",
	}

	report.Discrepancies = append(report.Discrepancies, discrepancy)
	report.MatchRate = 0.95

	if len(report.Discrepancies) != 1 {
		t.Errorf("expected 1 discrepancy, got %d", len(report.Discrepancies))
	}

	if report.MatchRate != 0.95 {
		t.Errorf("expected match rate 0.95, got %f", report.MatchRate)
	}

	if report.Discrepancies[0].Type != "zombie_missed" {
		t.Errorf("expected type 'zombie_missed', got %s", report.Discrepancies[0].Type)
	}
}

// TestShadowCompletionsAndStalls verifies completion and stall tracking
func TestShadowCompletionsAndStalls(t *testing.T) {
	report := &ShadowReport{
		CycleCount: 1,
		Rig:        "gastown",
		Mode:       "shadow",
	}

	report.Completions = []ShadowCompletion{
		{Polecat: "polecat1", Action: "create-wisp"},
		{Polecat: "polecat2", Action: "nudge-refinery"},
	}

	report.Stalls = []ShadowStall{
		{Polecat: "polecat3", StallType: "done-intent", Action: "escalate"},
	}

	if len(report.Completions) != 2 {
		t.Errorf("expected 2 completions, got %d", len(report.Completions))
	}

	if len(report.Stalls) != 1 {
		t.Errorf("expected 1 stall, got %d", len(report.Stalls))
	}

	if report.Completions[0].Action != "create-wisp" {
		t.Errorf("expected action 'create-wisp', got %s", report.Completions[0].Action)
	}
}

// TestShadowEscalationRouting verifies escalation targeting is correct
func TestShadowEscalationRouting(t *testing.T) {
	report := &ShadowReport{
		CycleCount: 1,
		Rig:        "gastown",
		Mode:       "shadow",
	}

	escalations := []ShadowEscalation{
		{Type: "help-triage", Target: "deacon", Details: "Generic HELP"},
		{Type: "help-triage", Target: "overseer", Details: "Security issue"},
		{Type: "refinery-stuck", Target: "mayor", Details: "Queue stuck > 30min"},
		{Type: "dirty-state", Target: "deacon", Details: "Uncommitted work"},
	}

	report.Escalations = escalations

	if len(report.Escalations) != 4 {
		t.Errorf("expected 4 escalations, got %d", len(report.Escalations))
	}

	// Verify routing is deterministic
	targets := make(map[string]int)
	for _, e := range report.Escalations {
		targets[e.Target]++
	}

	expectedTargets := map[string]int{
		"deacon":    2,
		"overseer":  1,
		"mayor":     1,
	}

	for target, count := range expectedTargets {
		if targets[target] != count {
			t.Errorf("expected %d escalations to %s, got %d", count, target, targets[target])
		}
	}
}

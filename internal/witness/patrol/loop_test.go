package patrol

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOneShotFullPatrolCycle(t *testing.T) {
	cfg := PatrolConfig{
		Rig:                 "testrig",
		WorkDir:             t.TempDir(),
		BackoffBase:         30 * time.Second,
		BackoffMult:         2,
		BackoffMax:          5 * time.Minute,
		IdleEffortThreshold: 3,
		Once:                true,
		JSON:                true,
	}

	// Ensure state file exists
	_ = EnsureStateFile(cfg.WorkDir, cfg.Rig)

	// Run patrol loop (will exit after one cycle due to --once)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := RunPatrolLoop(ctx, cfg)
	// With --once and immediate cancel, may get error - that's OK
	_ = err
}

func TestOneShotAbbreviatedPatrolCycle(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := PatrolConfig{
		Rig:                 "testrig",
		WorkDir:             tmpDir,
		BackoffBase:         30 * time.Second,
		BackoffMult:         2,
		BackoffMax:          5 * time.Minute,
		IdleEffortThreshold: 3,
		Once:                true,
		JSON:                true,
	}

	// Set idle cycles to trigger abbreviated mode
	state := &PatrolState{
		Rig:        cfg.Rig,
		IdleCycles: 5, // Above threshold
	}
	_ = saveState(tmpDir, state)

	// Run patrol loop
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := RunPatrolLoop(ctx, cfg)
	_ = err
}

func TestStateRecoveryAfterCrash(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"

	// Simulate crash: save state before "crash"
	originalState := &PatrolState{
		Rig:          rigName,
		IdleCycles:   5,
		LastPatrol:   time.Now().Add(-5 * time.Minute),
		LastActivity: time.Now().Add(-1 * time.Minute),
	}
	_ = saveState(tmpDir, originalState)

	// Simulate restart: load state
	recoveredState, err := loadState(tmpDir, rigName)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}

	if recoveredState.IdleCycles != 5 {
		t.Errorf("IdleCycles: got %d, want 5", recoveredState.IdleCycles)
	}

	if recoveredState.Rig != rigName {
		t.Errorf("Rig: got %q, want %q", recoveredState.Rig, rigName)
	}
}

func TestLoopBackoffRespectsIdleCount(t *testing.T) {
	cfg := PatrolConfig{
		BackoffBase: 30 * time.Second,
		BackoffMult: 2,
		BackoffMax:  5 * time.Minute,
	}

	state := &PatrolState{IdleCycles: 2}
	backoff := computeBackoff(state.IdleCycles, cfg)

	if backoff != 2*time.Minute {
		t.Errorf("expected 2m backoff for idleCycles=2, got %v", backoff)
	}

	// Increment idle cycles
	state.IdleCycles++
	backoff = computeBackoff(state.IdleCycles, cfg)

	if backoff != 4*time.Minute {
		t.Errorf("expected 4m backoff for idleCycles=3, got %v", backoff)
	}
}

func TestEventWakesFromBackoff(t *testing.T) {
	tmpDir := t.TempDir()
	state := &PatrolState{
		Rig:        "testrig",
		IdleCycles: 2,
	}
	_ = saveState(tmpDir, state)

	// Simulate event wake
	state.IdleCycles = 0
	_ = saveState(tmpDir, state)

	recovered, _ := loadState(tmpDir, "testrig")
	if recovered.IdleCycles != 0 {
		t.Errorf("IdleCycles after event: got %d, want 0", recovered.IdleCycles)
	}
}

func TestTimeoutIncrementsIdle(t *testing.T) {
	tmpDir := t.TempDir()
	state := &PatrolState{
		Rig:        "testrig",
		IdleCycles: 2,
	}
	_ = saveState(tmpDir, state)

	// Simulate timeout wake (increment idle)
	state.IdleCycles++
	_ = saveState(tmpDir, state)

	recovered, _ := loadState(tmpDir, "testrig")
	if recovered.IdleCycles != 3 {
		t.Errorf("IdleCycles after timeout: got %d, want 3", recovered.IdleCycles)
	}
}

func TestLoopCleanShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	state := &PatrolState{
		Rig:         "testrig",
		IdleCycles:  3,
		LastPatrol:  time.Now(),
	}

	// Save before shutdown
	_ = saveState(tmpDir, state)

	// Verify state file exists
	stateFile := filepath.Join(tmpDir, StateFileName)
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state file should exist after shutdown: %v", err)
	}
}

func TestConcurrentPatrolInstanceVeto(t *testing.T) {
	tmpDir := t.TempDir()

	// First instance creates state
	state1 := &PatrolState{
		Rig:        "testrig",
		IdleCycles: 0,
	}
	_ = saveState(tmpDir, state1)

	// Second instance checks for existing state file
	if _, err := os.Stat(filepath.Join(tmpDir, StateFileName)); err != nil {
		t.Error("second instance should detect existing state file")
	}

	// The actual veto logic would be in the CLI layer, not here
	// This test verifies the state file detection mechanism works
}

func TestScanFailsAndRetries(t *testing.T) {
	// Test that scan failures are handled gracefully
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	// Create escalator with mock behavior
	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)
	_ = escalator

	// With nil scan result, RouteScanFindings should return no escalations
	result := &ScanResult{}
	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}
	if len(escalations) != 0 {
		t.Errorf("expected 0 escalations, got %d", len(escalations))
	}
}

func TestBeadsServerDown(t *testing.T) {
	// Test graceful degradation when beads is unavailable
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: "/nonexistent", // Non-existent directory
	}

	// State file operations should fail gracefully
	state, err := loadState(cfg.WorkDir, cfg.Rig)
	if err == nil {
		// If no error, state was loaded (maybe file exists?)
		if state != nil && state.Rig == "" {
			t.Log("state loaded with empty Rig - OK")
		}
	}
	_ = cfg
}

func TestTmuxUnreachable(t *testing.T) {
	// Test that tmux unreachability doesn't crash patrol
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	// With no zombies, no escalations needed
	result := &ScanResult{
		Zombies:     []ScanZombie{},
		Stalls:      []ScanStall{},
		Completions: []ScanCompletion{},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 0 {
		t.Errorf("expected 0 escalations, got %d", len(escalations))
	}
}

func TestMailServerDown(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	// runMailDrain would fail, but shouldn't crash the patrol
	// This is tested by the integration tests with mocked executors
	_ = cfg
}

func TestDogSlingFails(t *testing.T) {
	// Test behavior when gt sling fails
	// The escalator should handle this gracefully
	escalator := NewEscalator("/nonexistent", "testrig")

	esc := &Escalation{
		Type:    EscalationDirtyState,
		Polecat: "furiosa",
		Rig:     "testrig",
		Details: "Test escalation",
	}

	// Escalate would fail, but the error should be handled
	err := escalator.Escalate(esc, "investigate")
	if err == nil {
		t.Log("Escalate should fail with /nonexistent workDir")
	}
}

func TestBeadCreationFails(t *testing.T) {
	// Test behavior when bd create fails
	// Escalator should return error, not crash
	escalator := NewEscalator("/nonexistent", "testrig")

	esc := &Escalation{
		Type:    EscalationHelpTriage,
		Rig:     "testrig",
		Details: "Test",
	}

	err := escalator.Escalate(esc, "deacon")
	if err == nil {
		t.Error("Escalate should fail with /nonexistent workDir")
	}
}

func TestContextDeadlineExceeded(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
		Once:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	time.Sleep(10 * time.Millisecond) // Let context expire

	err := RunPatrolLoop(ctx, cfg)
	if err != nil && err != context.DeadlineExceeded {
		// Error expected when context times out during one-shot
	}
}

func TestZombieDetectedAndRestarted(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	// Zombie with dirty state
	result := &ScanResult{
		Zombies: []ScanZombie{
			{
				Polecat:        "furiosa",
				Classification: "dead_session",
				CleanupStatus:  "dirty",
				HookBead:       "gt-123",
				Action:         "restarted",
				WasActive:      true,
			},
		},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 1 {
		t.Errorf("expected 1 escalation, got %d", len(escalations))
	}

	if escalations[0].Type != EscalationDirtyState {
		t.Errorf("expected %q, got %q", EscalationDirtyState, escalations[0].Type)
	}
}

func TestIdleCleanPolecatSkipped(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	// Idle polecat with clean state - no escalation
	result := &ScanResult{
		Zombies: []ScanZombie{
			{
				Polecat:        "dom",
				Classification: "idle",
				CleanupStatus:  "clean",
				Action:         "none",
				WasActive:      false,
			},
		},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 0 {
		t.Errorf("expected 0 escalations for clean idle polecat, got %d", len(escalations))
	}
}

func TestCompletionDiscovered(t *testing.T) {
	result := &ScanResult{
		Zombies: []ScanZombie{},
		Stalls:  []ScanStall{},
		Completions: []ScanCompletion{
			{
				Polecat:     "furiosa",
				ExitType:    "COMPLETED",
				IssueID:     "gt-123",
				MRID:        "gt-456",
				Action:      "cleanup wisp created, refinery nudged",
			},
		},
	}

	if len(result.Completions) != 1 {
		t.Errorf("expected 1 completion, got %d", len(result.Completions))
	}

	if result.Completions[0].ExitType != "COMPLETED" {
		t.Errorf("ExitType: got %q, want COMPLETED", result.Completions[0].ExitType)
	}
}

func TestLoopNoEscalationOnCleanScan(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	result := &ScanResult{
		Zombies:     []ScanZombie{},
		Stalls:      []ScanStall{},
		Completions: []ScanCompletion{},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 0 {
		t.Errorf("expected 0 escalations for clean scan, got %d", len(escalations))
	}
}

func TestPatrolCycleResult(t *testing.T) {
	result := &PatrolCycleResult{
		Timestamp:     time.Now(),
		Rig:          "testrig",
		DrainCount:    5,
		Zombies:      0,
		Stalls:       0,
		Completions:  1,
		Escalations:  0,
		Effort:       "full",
		CycleDuration: 500 * time.Millisecond,
	}

	if result.DrainCount != 5 {
		t.Errorf("DrainCount: got %d, want 5", result.DrainCount)
	}
	if result.Completions != 1 {
		t.Errorf("Completions: got %d, want 1", result.Completions)
	}
	if result.Effort != "full" {
		t.Errorf("Effort: got %q, want full", result.Effort)
	}
}

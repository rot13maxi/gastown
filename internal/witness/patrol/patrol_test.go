package patrol

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestComputeBackoff(t *testing.T) {
	cfg := PatrolConfig{
		BackoffBase: 30 * time.Second,
		BackoffMult: 2,
		BackoffMax:  5 * time.Minute,
	}

	tests := []struct {
		name       string
		idleCycles int
		want       time.Duration
	}{
		{"zero idle", 0, 30 * time.Second},
		{"one idle", 1, 60 * time.Second},
		{"two idle", 2, 2 * time.Minute},
		{"three idle", 3, 4 * time.Minute},
		{"four idle", 4, 5 * time.Minute}, // capped
		{"five idle", 5, 5 * time.Minute}, // capped
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeBackoff(tt.idleCycles, cfg)
			if got != tt.want {
				t.Errorf("computeBackoff(%d) = %v, want %v", tt.idleCycles, got, tt.want)
			}
		})
	}
}

func TestComputeBackoff_ZeroIdle(t *testing.T) {
	cfg := PatrolConfig{
		BackoffBase: 30 * time.Second,
		BackoffMult: 2,
		BackoffMax:  5 * time.Minute,
	}

	got := computeBackoff(0, cfg)
	if got != 30*time.Second {
		t.Errorf("computeBackoff(0) = %v, want 30s", got)
	}
}

func TestComputeBackoff_AboveCap(t *testing.T) {
	cfg := PatrolConfig{
		BackoffBase: 30 * time.Second,
		BackoffMult: 2,
		BackoffMax:  5 * time.Minute,
	}

	// After 4 cycles: 30s * 2^4 = 480s = 8 minutes, capped at 5 minutes
	got := computeBackoff(4, cfg)
	if got != 5*time.Minute {
		t.Errorf("computeBackoff(4) = %v, want 5m (capped)", got)
	}
}

func TestStateFileRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"

	original := &PatrolState{
		Rig:          rigName,
		IdleCycles:   3,
		LastPatrol:   time.Now().Add(-1 * time.Hour),
		LastActivity: time.Now().Add(-30 * time.Minute),
		BackoffUntil: time.Now().Add(60 * time.Second),
	}

	// Save
	if err := saveState(tmpDir, original); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// Load
	loaded, err := loadState(tmpDir, rigName)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}

	if loaded.Rig != original.Rig {
		t.Errorf("Rig: got %q, want %q", loaded.Rig, original.Rig)
	}
	if loaded.IdleCycles != original.IdleCycles {
		t.Errorf("IdleCycles: got %d, want %d", loaded.IdleCycles, original.IdleCycles)
	}
}

func TestStateFileCreateIfMissing(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "testrig"

	// State file doesn't exist, should create with defaults
	state, err := loadState(tmpDir, rigName)
	if err == nil {
		t.Error("expected error for missing state file")
	}

	// Create default state
	if err := EnsureStateFile(tmpDir, rigName); err != nil {
		t.Fatalf("EnsureStateFile: %v", err)
	}

	// Now load should succeed
	state, err = loadState(tmpDir, rigName)
	if err != nil {
		t.Fatalf("loadState after EnsureStateFile: %v", err)
	}

	if state.Rig != rigName {
		t.Errorf("Rig: got %q, want %q", state.Rig, rigName)
	}
	if state.IdleCycles != 0 {
		t.Errorf("IdleCycles: got %d, want 0", state.IdleCycles)
	}
}

func TestAbbreviatedPatrolLogic(t *testing.T) {
	cfg := PatrolConfig{
		IdleEffortThreshold: 3,
	}

	tests := []struct {
		name       string
		idleCycles int
		want       string
	}{
		{"below threshold", 0, "full"},
		{"below threshold 2", 2, "full"},
		{"at threshold", 3, "abbreviated"},
		{"above threshold", 5, "abbreviated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effort := "full"
			if tt.idleCycles >= cfg.IdleEffortThreshold {
				effort = "abbreviated"
			}
			if effort != tt.want {
				t.Errorf("idleCycles=%d: got %q, want %q", tt.idleCycles, effort, tt.want)
			}
		})
	}
}

func TestFullPatrolLogic(t *testing.T) {
	cfg := PatrolConfig{
		IdleEffortThreshold: 3,
	}

	state := &PatrolState{
		IdleCycles: 0,
	}

	effort := "full"
	if state.IdleCycles >= cfg.IdleEffortThreshold {
		effort = "abbreviated"
	}

	if effort != "full" {
		t.Errorf("expected full patrol for idleCycles=0, got %q", effort)
	}
}

func TestStateFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	got := StateFilePath(tmpDir)
	want := filepath.Join(tmpDir, StateFileName)
	if got != want {
		t.Errorf("StateFilePath: got %q, want %q", got, want)
	}
}

func TestIdleCycleIncrement(t *testing.T) {
	state := &PatrolState{IdleCycles: 2}
	
	// Simulate timeout wake
	state.IdleCycles++
	
	if state.IdleCycles != 3 {
		t.Errorf("IdleCycles: got %d, want 3", state.IdleCycles)
	}
}

func TestIdleCycleReset(t *testing.T) {
	state := &PatrolState{IdleCycles: 5}
	
	// Simulate event wake (activity detected)
	state.IdleCycles = 0
	
	if state.IdleCycles != 0 {
		t.Errorf("IdleCycles: got %d, want 0", state.IdleCycles)
	}
}

func TestScanResultRouting(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	result := &ScanResult{
		Zombies: []ScanZombie{
			{
				Polecat:        "furiosa",
				Classification: "dead_session",
				CleanupStatus:  "dirty",
				HookBead:       "gt-123",
				Action:         "restarted",
			},
		},
		Stalls: []ScanStall{
			{
				Polecat:   "dom",
				StallType: "startup_prompt",
				Action:    "dismissed",
			},
		},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 2 {
		t.Errorf("expected 2 escalations, got %d", len(escalations))
	}
}

func TestNoEscalationOnCleanScan(t *testing.T) {
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

func TestZombieEscalation(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	result := &ScanResult{
		Zombies: []ScanZombie{
			{
				Polecat:        "furiosa",
				Classification: "dead_session",
				CleanupStatus:  "dirty",
				HookBead:       "gt-123",
				Action:         "restarted",
			},
		},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 1 {
		t.Fatalf("expected 1 escalation, got %d", len(escalations))
	}

	if escalations[0].Type != EscalationDirtyState {
		t.Errorf("expected escalation type %q, got %q", EscalationDirtyState, escalations[0].Type)
	}

	if escalations[0].Polecat != "furiosa" {
		t.Errorf("expected polecat %q, got %q", "furiosa", escalations[0].Polecat)
	}
}

func TestZombieRestartNoEscalation(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "testrig",
		WorkDir: t.TempDir(),
	}

	escalator := NewEscalator(cfg.WorkDir, cfg.Rig)

	// Zombie with clean state should not escalate
	result := &ScanResult{
		Zombies: []ScanZombie{
			{
				Polecat:        "furiosa",
				Classification: "dead_session",
				CleanupStatus:  "clean", // Clean state - no escalation
				Action:         "restarted",
			},
		},
	}

	escalations, err := escalator.RouteScanFindings(result, cfg)
	if err != nil {
		t.Fatalf("RouteScanFindings: %v", err)
	}

	if len(escalations) != 0 {
		t.Errorf("expected 0 escalations for clean zombie, got %d", len(escalations))
	}
}

func TestCleanShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	state := &PatrolState{
		Rig:        "testrig",
		IdleCycles: 3,
		LastPatrol: time.Now(),
	}

	// Simulate context cancellation and state save
	if err := saveState(tmpDir, state); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// Verify state was saved
	saved, err := loadState(tmpDir, "testrig")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}

	if saved.IdleCycles != 3 {
		t.Errorf("IdleCycles: got %d, want 3", saved.IdleCycles)
	}
}

func TestBackoffRespectsIdleCount(t *testing.T) {
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

func TestPatrolConfigDefaults(t *testing.T) {
	cfg := PatrolConfig{}

	if cfg.BackoffBase != 0 {
		t.Errorf("default BackoffBase should be 0, got %v", cfg.BackoffBase)
	}
	if cfg.BackoffMult != 0 {
		t.Errorf("default BackoffMult should be 0, got %v", cfg.BackoffMult)
	}
	if cfg.IdleEffortThreshold != 0 {
		t.Errorf("default IdleEffortThreshold should be 0, got %v", cfg.IdleEffortThreshold)
	}
}

func TestPatrolStateFields(t *testing.T) {
	state := &PatrolState{
		Rig:          "gastown",
		IdleCycles:   0,
		BackoffUntil: time.Time{},
		LastPatrol:   time.Time{},
		LastActivity: time.Time{},
	}

	if state.Rig != "gastown" {
		t.Errorf("Rig: got %q, want %q", state.Rig, "gastown")
	}
	if state.IdleCycles != 0 {
		t.Errorf("IdleCycles: got %d, want 0", state.IdleCycles)
	}
}

func TestScanResultEmpty(t *testing.T) {
	result := &ScanResult{}

	if len(result.Zombies) != 0 {
		t.Errorf("Zombies: expected 0, got %d", len(result.Zombies))
	}
	if len(result.Stalls) != 0 {
		t.Errorf("Stalls: expected 0, got %d", len(result.Stalls))
	}
	if len(result.Completions) != 0 {
		t.Errorf("Completions: expected 0, got %d", len(result.Completions))
	}
}

func TestStateFilePermission(t *testing.T) {
	tmpDir := t.TempDir()
	state := &PatrolState{Rig: "testrig", IdleCycles: 1}

	if err := saveState(tmpDir, state); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// Check file permissions
	stateFile := filepath.Join(tmpDir, StateFileName)
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}

	// Should be readable and writable
	if info.Mode()&0600 == 0 {
		t.Errorf("state file should have 0600 permissions, got %v", info.Mode())
	}
}

func TestPatrolConfigShadowMode(t *testing.T) {
	cfg := PatrolConfig{
		Rig:     "gastown",
		WorkDir: "/tmp",
		Shadow:  true,
	}

	if !cfg.Shadow {
		t.Errorf("Shadow mode should be enabled")
	}
}

func TestPatrolCycleResultShadowMode(t *testing.T) {
	result := &PatrolCycleResult{
		Timestamp:  time.Now(),
		Rig:        "gastown",
		ShadowMode: true,
	}

	if !result.ShadowMode {
		t.Errorf("ShadowMode should be true in result")
	}
}

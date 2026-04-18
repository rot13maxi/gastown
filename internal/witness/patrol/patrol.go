// Package patrol provides a deterministic Go-based patrol loop that replaces
// the mol-witness-patrol Claude Code molecule. All detection logic in
// internal/witness/handlers.go is reused unchanged; this package wraps it
// into a loop with backoff and state management.
//
// The patrol loop:
//   - Runs mail drain (deterministic)
//   - Runs patrol scan --json (calls existing Go CLI)
//   - Routes scan findings (deterministic escalation routing)
//   - Checks timer gates and swarm completion
//   - Writes patrol receipts as ephemeral wisps
//   - Awaits next trigger (event or backoff timeout)
//
// When it hits a case it cannot resolve by rule, it slings a wisp to a deacon
// dog, reusing the existing dog infrastructure.
package patrol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/channelevents"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/util"
)

// Default values
const (
	DefaultBackoffBase         = 30 * time.Second
	DefaultBackoffMult         = 2
	DefaultBackoffMax          = 5 * time.Minute
	DefaultIdleEffortThreshold = 3
)

// PatrolConfig holds configuration for the patrol loop.
type PatrolConfig struct {
	Rig                 string
	WorkDir             string
	TownRoot            string
	BackoffBase         time.Duration
	BackoffMult         int
	BackoffMax          time.Duration
	IdleEffortThreshold int
	Once                bool // Run one cycle and exit
	JSON                bool // JSON output for --once mode
	Verbose             bool
	Escalator           *Escalator
}

// PatrolState tracks state across patrol cycles.
type PatrolState struct {
	Rig          string    `json:"rig"`
	IdleCycles   int       `json:"idle_cycles"`
	BackoffUntil time.Time `json:"backoff_until,omitempty"`
	LastPatrol   time.Time `json:"last_patrol,omitempty"`
	LastActivity time.Time `json:"last_activity,omitempty"`
}

// PatrolCycleResult is the result of a single patrol cycle.
type PatrolCycleResult struct {
	ID            string         `json:"id,omitempty"`
	Timestamp     time.Time      `json:"timestamp"`
	Rig           string         `json:"rig"`
	DrainCount    int            `json:"drain_count"`
	Zombies       int            `json:"zombies"`
	Stalls        int            `json:"stalls"`
	Completions   int            `json:"completions"`
	Escalations   int            `json:"escalations"`
	Effort        string         `json:"effort"` // "full" or "abbreviated"
	CycleDuration time.Duration  `json:"cycle_duration"`
	Error         string         `json:"error,omitempty"`
}

// StateFileName is the filename for the patrol state file.
const StateFileName = "witness-patrol-state.json"

// RunPatrolLoop runs the main patrol loop until context is cancelled.
func RunPatrolLoop(ctx context.Context, cfg PatrolConfig) error {
	state, err := loadState(cfg.WorkDir, cfg.Rig)
	if err != nil {
		// Create default state if file doesn't exist
		state = &PatrolState{
			Rig:        cfg.Rig,
			IdleCycles: 0,
		}
	}

	for {
		select {
		case <-ctx.Done():
			return saveState(cfg.WorkDir, state)
		default:
		}

		cycleResult, err := runOneCycle(ctx, cfg, state)
		if cfg.Once {
			if cfg.JSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(cycleResult)
			}
			if err != nil {
				return err
			}
			return nil
		}

		if err != nil {
			// Log error but continue patrol
			fmt.Fprintf(os.Stderr, "patrol: cycle error: %v\n", err)
			cycleResult.Error = err.Error()
		}

		// Update state
		state.LastPatrol = cycleResult.Timestamp

		// Save state
		if err := saveState(cfg.WorkDir, state); err != nil {
			fmt.Fprintf(os.Stderr, "patrol: failed to save state: %v\n", err)
		}

		// Await next trigger
		idleStr := "full"
		if state.IdleCycles >= cfg.IdleEffortThreshold {
			idleStr = "abbreviated"
		}

		wasTimeout, err := awaitNextTrigger(state, idleStr, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return saveState(cfg.WorkDir, state)
			}
			fmt.Fprintf(os.Stderr, "patrol: await error: %v\n", err)
		}

		// Update idle cycles based on wake type
		if wasTimeout {
			state.IdleCycles++
		} else {
			state.IdleCycles = 0
		}
	}
}

// runOneCycle executes a single patrol cycle.
func runOneCycle(ctx context.Context, cfg PatrolConfig, state *PatrolState) (*PatrolCycleResult, error) {
	cycleStarted := time.Now()
	result := &PatrolCycleResult{
		Timestamp: cycleStarted,
		Rig:       cfg.Rig,
	}

	effort := "full"
	if state.IdleCycles >= cfg.IdleEffortThreshold {
		effort = "abbreviated"
	}
	result.Effort = effort

	// 1. Mail drain (deterministic)
	drainCount, err := runMailDrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "patrol: warning: mail drain failed: %v\n", err)
	}
	result.DrainCount = drainCount

	if effort == "abbreviated" {
		// Abbreviated patrol: drain + quick scan only
		if _, err := runPatrolScanJSON(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "patrol: abbreviated: scan failed: %v\n", err)
		}
		result.CycleDuration = time.Since(cycleStarted)
		return result, nil
	}

	// 2. Run full patrol scan (calls existing Go CLI)
	scanResult, err := runPatrolScanJSON(cfg)
	if err != nil {
		return nil, fmt.Errorf("patrol scan failed: %w", err)
	}

	result.Zombies = len(scanResult.Zombies)
	result.Stalls = len(scanResult.Stalls)
	result.Completions = len(scanResult.Completions)

	// 3. Route scan findings (deterministic)
	if cfg.Escalator != nil {
		escalations, err := cfg.Escalator.RouteScanFindings(scanResult, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "patrol: warning: escalation routing failed: %v\n", err)
		}
		result.Escalations = len(escalations)
	}

	// 4. Check timer gates
	if err := checkTimerGates(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "patrol: timer gate check failed: %v\n", err)
	}

	// 5. Check swarm completion
	if err := checkSwarmCompletion(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "patrol: swarm check failed: %v\n", err)
	}

	result.CycleDuration = time.Since(cycleStarted)

	return result, nil
}

// runMailDrain runs gt mail drain for the witness.
func runMailDrain(cfg PatrolConfig) (int, error) {
	args := []string{"mail", "drain", "--identity", fmt.Sprintf("%s/witness", cfg.Rig), "--max-age", "30m"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		return 0, fmt.Errorf("mail drain: %w", err)
	}

	// Parse drain count from output
	var drainCount int
	fmt.Sscanf(output, "%d", &drainCount) // May be 0 if parsing fails
	return drainCount, nil
}

// ScanResult represents the output of gt patrol scan --json.
type ScanResult struct {
	Rig         string                   `json:"rig"`
	Timestamp   string                   `json:"timestamp"`
	Zombies     []ScanZombie             `json:"zombies"`
	Stalls      []ScanStall              `json:"stalls"`
	Completions []ScanCompletion         `json:"completions"`
	Receipts    []map[string]interface{} `json:"receipts,omitempty"`
}

type ScanZombie struct {
	Polecat        string `json:"polecat"`
	Classification string `json:"classification"`
	AgentState     string `json:"agent_state"`
	HookBead       string `json:"hook_bead,omitempty"`
	CleanupStatus  string `json:"cleanup_status,omitempty"`
	Action         string `json:"action"`
	WasActive      bool   `json:"was_active"`
	Error          string `json:"error,omitempty"`
}

type ScanStall struct {
	Polecat   string `json:"polecat"`
	StallType string `json:"stall_type"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}

type ScanCompletion struct {
	Polecat        string `json:"polecat"`
	ExitType       string `json:"exit_type"`
	IssueID        string `json:"issue_id,omitempty"`
	MRID           string `json:"mr_id,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Action         string `json:"action"`
	WispCreated    string `json:"wisp_created,omitempty"`
	CompletionTime string `json:"completion_time,omitempty"`
}

// runPatrolScanJSON runs gt patrol scan --json and parses the result.
func runPatrolScanJSON(cfg PatrolConfig) (*ScanResult, error) {
	args := []string{"patrol", "scan", "--rig", cfg.Rig, "--json"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		return nil, fmt.Errorf("patrol scan: %w", err)
	}

	var result ScanResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("parsing scan JSON: %w", err)
	}

	return &result, nil
}

// checkTimerGates runs bd gate check --type=timer --escalate.
func checkTimerGates(cfg PatrolConfig) error {
	args := []string{"gate", "check", "--type=timer", "--escalate"}
	_, err := util.ExecWithOutput(cfg.WorkDir, "bd", args...)
	if err != nil {
		return fmt.Errorf("timer gate check: %w", err)
	}
	return nil
}

// checkSwarmCompletion checks if active swarms are complete.
func checkSwarmCompletion(cfg PatrolConfig) error {
	// Find active swarm wisps
	args := []string{"list", "--label=swarm", "--status=open", "--json"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "bd", args...)
	if err != nil {
		return fmt.Errorf("swarm list: %w", err)
	}

	var swarms []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Labels string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(output), &swarms); err != nil {
		return fmt.Errorf("parsing swarm list: %w", err)
	}

	for _, swarm := range swarms {
		// Check if all polecats in swarm have merged
		completed, total, err := countSwarmCompletions(cfg, swarm.ID, swarm.Labels)
		if err != nil {
			fmt.Fprintf(os.Stderr, "patrol: swarm completion check failed: %v\n", err)
			continue
		}
		if completed >= total && total > 0 {
			// Notify mayor
			notifyMayorSwarmComplete(cfg, swarm.Title, completed, total)
			// Close swarm wisp
			closeSwarmWisp(cfg, swarm.ID)
		}
	}

	return nil
}

func countSwarmCompletions(cfg PatrolConfig, swarmID, labels string) (int, int, error) {
	// Parse total from labels (e.g., "total:5")
	var total, completed int
	fmt.Sscanf(labels, "%*[^,],total:%d,%*s", &total)

	// Count cleanup wisps for this swarm's polecats
	args := []string{"list", "--label=cleanup,swarm:" + swarmID, "--status=closed", "--json"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "bd", args...)
	if err != nil {
		return 0, total, nil // Non-fatal
	}

	var closed []struct{ ID string }
	if err := json.Unmarshal([]byte(output), &closed); err != nil {
		return 0, total, nil
	}
	completed = len(closed)

	return completed, total, nil
}

func notifyMayorSwarmComplete(cfg PatrolConfig, swarmTitle string, completed, total int) {
	msg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", cfg.Rig),
		To:      "mayor/",
		Subject: fmt.Sprintf("SWARM_COMPLETE: %s", swarmTitle),
		Body:    fmt.Sprintf("All %d polecats merged.\nSwarm: %s", total, swarmTitle),
	}
	router := mail.NewRouter(cfg.TownRoot)
	_ = router.Send(msg)
}

func closeSwarmWisp(cfg PatrolConfig, wispID string) {
	args := []string{"close", wispID, "--reason=all polecats merged"}
	_ = util.ExecRun(cfg.WorkDir, "bd", args...)
}

// awaitNextTrigger waits for the next trigger (event or backoff timeout).
// Returns true if it was a timeout, false if an event woke it.
func awaitNextTrigger(state *PatrolState, effort string, cfg PatrolConfig) (bool, error) {
	// Compute backoff
	backoff := computeBackoff(state.IdleCycles, cfg)
	state.BackoffUntil = time.Now().Add(backoff)

	// Emit tick event so polecat activity wakes us
	if cfg.TownRoot != "" {
		_, _ = channelevents.EmitToTown(cfg.TownRoot, "witness", "PATROL_TICK", []string{
			"rig=" + cfg.Rig,
			"effort=" + effort,
		})
	}

	// Use await-event CLI
	args := []string{
		"mol", "step", "await-event",
		"--channel", "witness",
		"--timeout", backoff.String(),
		"--backoff-base", cfg.BackoffBase.String(),
		"--backoff-mult", fmt.Sprintf("%d", cfg.BackoffMult),
		"--backoff-max", cfg.BackoffMax.String(),
		"--quiet",
		"--json",
	}

	_, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		// Check if it's a timeout (expected) or real error
		// await-event returns non-zero on timeout, which is expected
		return true, nil
	}

	return false, nil
}

// computeBackoff computes the backoff duration based on idle cycle count.
func computeBackoff(idleCycles int, cfg PatrolConfig) time.Duration {
	backoff := cfg.BackoffBase
	for i := 0; i < idleCycles; i++ {
		backoff *= time.Duration(cfg.BackoffMult)
		if backoff > cfg.BackoffMax {
			return cfg.BackoffMax
		}
	}
	return backoff
}

// loadState loads patrol state from the state file.
func loadState(workDir, rigName string) (*PatrolState, error) {
	stateFile := filepath.Join(workDir, StateFileName)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}

	var state PatrolState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	if state.Rig == "" {
		state.Rig = rigName
	}

	return &state, nil
}

// saveState saves patrol state to the state file.
func saveState(workDir string, state *PatrolState) error {
	stateFile := filepath.Join(workDir, StateFileName)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		return fmt.Errorf("writing state file: %w", err)
	}

	return nil
}

// StateFilePath returns the path to the state file for a work directory.
func StateFilePath(workDir string) string {
	return filepath.Join(workDir, StateFileName)
}

// EnsureStateFile creates a default state file if it doesn't exist.
func EnsureStateFile(workDir, rigName string) error {
	stateFile := StateFilePath(workDir)
	if _, err := os.Stat(stateFile); err == nil {
		return nil // File exists
	}

	state := &PatrolState{
		Rig:        rigName,
		IdleCycles: 0,
	}

	return saveState(workDir, state)
}

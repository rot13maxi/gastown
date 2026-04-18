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
	"regexp"
	"strconv"
	"strings"
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

// logf logs a message to stderr. In shadow mode, all actions are prefixed with SHADOW:.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

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
	Shadow              bool // Shadow mode: log actions without taking them
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
	ShadowMode    bool           `json:"shadow_mode"`
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
		Timestamp:  cycleStarted,
		Rig:        cfg.Rig,
		ShadowMode: cfg.Shadow,
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

	// 2. Process HELP messages from inbox (non-drainable messages need routing)
	if err := processDrainedMessages(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "patrol: warning: HELP message processing failed: %v\n", err)
	}

	if effort == "abbreviated" {
		// Abbreviated patrol: drain + quick scan only
		if cfg.Shadow {
			logf("SHADOW: running abbreviated patrol (drain + scan only)")
		}
		if _, err := runPatrolScanJSON(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "patrol: abbreviated: scan failed: %v\n", err)
		}
		result.CycleDuration = time.Since(cycleStarted)
		return result, nil
	}

	// 3. Run full patrol scan (calls existing Go CLI)
	scanResult, err := runPatrolScanJSON(cfg)
	if err != nil {
		return nil, fmt.Errorf("patrol scan failed: %w", err)
	}

	result.Zombies = len(scanResult.Zombies)
	result.Stalls = len(scanResult.Stalls)
	result.Completions = len(scanResult.Completions)

	// 3. Route scan findings and execute escalations (deterministic)
	if cfg.Escalator != nil {
		escalations, err := cfg.Escalator.RouteScanFindings(scanResult, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "patrol: warning: escalation routing failed: %v\n", err)
		}
		for _, esc := range escalations {
			dogName := RouteDogForEscalation(esc)
			if cfg.Shadow {
				logf("SHADOW: would escalate %s to %s dog: %s", esc.Type, dogName, esc.Details)
			} else {
				if err := cfg.Escalator.Escalate(esc, dogName); err != nil {
					fmt.Fprintf(os.Stderr, "patrol: warning: escalation failed: %v\n", err)
				}
			}
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

	// 6. Check refinery health
	if err := checkRefineryHealth(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "patrol: refinery health check failed: %v\n", err)
	}

	result.CycleDuration = time.Since(cycleStarted)

	return result, nil
}

// runMailDrain runs gt mail drain for the witness.
// Returns the number of messages drained.
func runMailDrain(cfg PatrolConfig) (int, error) {
	args := []string{"mail", "drain", "--identity", fmt.Sprintf("%s/witness", cfg.Rig), "--max-age", "30m"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		return 0, fmt.Errorf("mail drain: %w", err)
	}

	// Parse drain count from output (format: "Drained N messages" or similar)
	drainCount, err := parseDrainCount(output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "patrol: warning: could not parse drain count from output: %v\n", err)
		return 0, nil
	}
	return drainCount, nil
}

// processDrainedMessages processes HELP messages from the witness inbox.
// HELP messages are preserved during drain and need routing to appropriate handlers.
func processDrainedMessages(cfg PatrolConfig) error {
	if cfg.Shadow {
		logf("SHADOW: would process HELP messages from inbox")
		return nil
	}

	// Create mailbox for witness identity
	mailbox := mail.NewMailboxFromAddress(fmt.Sprintf("%s/witness", cfg.Rig), cfg.WorkDir)

	// List unread messages
	messages, err := mailbox.ListUnread()
	if err != nil {
		// Non-fatal: mailbox may be empty or inaccessible
		return nil
	}

	// Process HELP messages
	for _, msg := range messages {
		if isHelpMessage(msg) {
			if cfg.Escalator != nil {
				if err := cfg.Escalator.EscalateHelpMessage(msg); err != nil {
					fmt.Fprintf(os.Stderr, "patrol: warning: failed to escalate HELP message %s: %v\n", msg.ID, err)
				} else {
					// Mark as read after successful escalation
					_ = mailbox.MarkRead(msg.ID)
				}
			}
		}
	}

	return nil
}

// isHelpMessage returns true if the message is a HELP request.
// HELP messages have "HELP" in the subject or body.
func isHelpMessage(msg *mail.Message) bool {
	if strings.Contains(strings.ToUpper(msg.Subject), "HELP") {
		return true
	}
	if strings.Contains(strings.ToUpper(msg.Body), "HELP") {
		return true
	}
	return false
}

// parseDrainCount parses the number of messages drained from gt mail drain output.
func parseDrainCount(output string) (int, error) {
	// Try to match "Drained N messages" or "N" at start
	patterns := []string{
		`Drained (\d+) messages?`,
		`Archived (\d+) messages?`,
		`^(\d+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(output)
		if len(matches) >= 2 {
			return strconv.Atoi(matches[1])
		}
	}

	return 0, fmt.Errorf("could not parse drain count from: %s", output)
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
	if cfg.Shadow {
		logf("SHADOW: would run 'bd gate check --type=timer --escalate'")
		return nil
	}
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
			if cfg.Shadow {
				logf("SHADOW: would notify mayor of swarm complete: %s (%d/%d)", swarm.Title, completed, total)
				logf("SHADOW: would close swarm wisp: %s", swarm.ID)
			} else {
				// Notify mayor
				notifyMayorSwarmComplete(cfg, swarm.Title, completed, total)
				// Close swarm wisp
				closeSwarmWisp(cfg, swarm.ID)
			}
		}
	}

	return nil
}

func countSwarmCompletions(cfg PatrolConfig, swarmID, labels string) (int, int, error) {
	// Parse total from labels using JSON parsing (format: "key:val,total:N,...")
	// Labels may not be valid JSON, so parse manually with regex
	total := parseLabelInt(labels, "total")

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
	completed := len(closed)

	return completed, total, nil
}

// parseLabelInt parses an integer value from a label string (e.g., "total:5,key:val").
func parseLabelInt(labels, key string) int {
	pattern := key + ":(\\d+)"
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(labels)
	if len(matches) >= 2 {
		if val, err := strconv.Atoi(matches[1]); err == nil {
			return val
		}
	}
	return 0
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

	output, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		// Real error (exit 1) — not a timeout. Log and return false so
		// idle cycles are NOT incremented on real errors (network, JSON
		// parse, permission, etc.). Only expected timeouts advance idle.
		fmt.Fprintf(os.Stderr, "patrol: await-event error (not a timeout): %v\n", err)
		return false, nil
	}

	// Exit 0 means JSON output. Parse reason to distinguish timeout from event.
	var result struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(output), &result) == nil && result.Reason == "timeout" {
		return true, nil
	}
	// Event woke us, or unknown reason — treat as non-timeout (no idle advance).
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

// RouteDogForEscalation determines the appropriate dog for an escalation type.
func RouteDogForEscalation(esc *Escalation) string {
	switch esc.Type {
	case EscalationRefineryStuck:
		return "investigate"
	case EscalationTimerGate:
		return "investigate"
	case EscalationDirtyState:
		return "investigate"
	default:
		return "investigate"
	}
}

// checkRefineryHealth checks if the refinery is healthy and escalates if stuck.
func checkRefineryHealth(cfg PatrolConfig) error {
	if cfg.Escalator == nil {
		return nil
	}

	// Check refinery status via gt refinery status
	args := []string{"refinery", "status", "--json"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		// Refinery may be down — this is the stuck condition
		// Check if there are MRs in the queue
		queueDepth := getRefineryQueueDepth(cfg)
		if queueDepth > 0 {
			if cfg.Shadow {
				logf("SHADOW: would escalate refinery stuck (queue=%d, minutes=0)", queueDepth)
			} else {
				if err := cfg.Escalator.EscalateRefineryStuck(queueDepth, 0); err != nil {
					return fmt.Errorf("refinery stuck escalation: %w", err)
				}
			}
		}
		return nil
	}

	// Parse refinery status for stuck detection
	var status struct {
		Stuck       bool `json:"stuck"`
		StuckMinutes int `json:"stuck_minutes"`
		QueueDepth  int  `json:"queue_depth"`
	}
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return nil // Non-fatal: status parsing failed
	}

	if status.Stuck && status.QueueDepth > 0 {
		if cfg.Shadow {
			logf("SHADOW: would escalate refinery stuck (queue=%d, minutes=%d)", status.QueueDepth, status.StuckMinutes)
		} else {
			if err := cfg.Escalator.EscalateRefineryStuck(status.QueueDepth, status.StuckMinutes); err != nil {
				return fmt.Errorf("refinery stuck escalation: %w", err)
			}
		}
	}

	return nil
}

// getRefineryQueueDepth returns the number of MRs in the refinery queue.
func getRefineryQueueDepth(cfg PatrolConfig) int {
	args := []string{"refinery", "queue", "--count"}
	output, err := util.ExecWithOutput(cfg.WorkDir, "gt", args...)
	if err != nil {
		return 0
	}

	// Try to parse a number from the output
	if match := regexp.MustCompile(`(\d+)`).FindStringSubmatch(output); len(match) >= 2 {
		if count, err := strconv.Atoi(match[1]); err == nil {
			return count
		}
	}
	return 0
}

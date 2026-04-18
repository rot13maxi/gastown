package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/witness/patrol"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Patrol command flags
var (
	patrolBackoffBase       time.Duration
	patrolBackoffMult       int
	patrolBackoffMax        time.Duration
	patrolIdleThreshold     int
	patrolOnce              bool
	patrolJSON              bool
	patrolVerbose           bool
	patrolShadow            bool
	patrolShadowLogFile     string
)

var witnessPatrolCmd = &cobra.Command{
	Use:   "patrol <rig>",
	Short: "Run the witness patrol loop (Go program)",
	Long: `Run the deterministic Go-based patrol loop that monitors polecats.

This command replaces mol-witness-patrol with a ~200-line Go program that calls
the same existing tools (gt patrol scan, gt mail drain, gt mol step await-event)
directly without spawning an AI agent.

The patrol loop:
  - gt mail drain (every cycle)
  - gt patrol scan --json (every cycle)
  - Route scan findings (deterministic escalation)
  - Check timer gates and swarm completion
  - gt mol step await-event --backoff (event or timeout)

When it hits a case it cannot resolve, it slings a wisp to a deacon dog.

Examples:
  gt witness patrol gastown
  gt witness patrol gastown --backoff-base 30s --backoff-max 5m
  gt witness patrol gastown --once --json  # Test one cycle
	gt witness patrol gastown --shadow  # Shadow mode: log actions without taking them (for validation)

Shadow Mode:
  gt witness patrol gastown --shadow  # Run alongside molecule, compare outputs
  gt witness patrol gastown --shadow --shadow-log /var/log/patrol-shadow.jsonl

Shadow mode logs all actions without taking them, allowing validation against
the existing molecule. Run for 48 hours and compare outputs.
`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessPatrol,
}

func init() {
	// Patrol flags
	witnessPatrolCmd.Flags().DurationVar(&patrolBackoffBase, "backoff-base", 30*time.Second, "Base backoff interval")
	witnessPatrolCmd.Flags().IntVar(&patrolBackoffMult, "backoff-mult", 2, "Backoff multiplier")
	witnessPatrolCmd.Flags().DurationVar(&patrolBackoffMax, "backoff-max", 5*time.Minute, "Max backoff interval")
	witnessPatrolCmd.Flags().IntVar(&patrolIdleThreshold, "idle-threshold", 3, "Idle cycles before abbreviated patrol")
	witnessPatrolCmd.Flags().BoolVar(&patrolOnce, "once", false, "Run one cycle and exit (for testing)")
	witnessPatrolCmd.Flags().BoolVar(&patrolJSON, "json", false, "JSON output (for --once mode)")
	witnessPatrolCmd.Flags().BoolVarP(&patrolVerbose, "verbose", "v", false, "Verbose logging")
	witnessPatrolCmd.Flags().BoolVar(&patrolShadow, "shadow", false, "Shadow mode: log all actions without taking them (for validation against molecule)")
	witnessPatrolCmd.Flags().StringVar(&patrolShadowLogFile, "shadow-log", "", "File to write shadow reports to (default: stdout)")

	witnessCmd.AddCommand(witnessPatrolCmd)
}

func runWitnessPatrol(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	workDir := townRoot

	cfg := patrol.PatrolConfig{
		Rig:                 rigName,
		WorkDir:             workDir,
		TownRoot:            townRoot,
		BackoffBase:         patrolBackoffBase,
		BackoffMult:         patrolBackoffMult,
		BackoffMax:          patrolBackoffMax,
		IdleEffortThreshold: patrolIdleThreshold,
		Once:                patrolOnce,
		JSON:                patrolJSON,
		Verbose:             patrolVerbose,
		Shadow:              patrolShadow,
		ShadowLogFile:       patrolShadowLogFile,
	}

	if cfg.Shadow {
		fmt.Fprintf(os.Stderr, "patrol: starting in SHADOW mode\n")
		fmt.Fprintf(os.Stderr, "patrol: shadow mode logs all actions without taking them\n")
		if cfg.ShadowLogFile != "" {
			fmt.Fprintf(os.Stderr, "patrol: shadow log: %s\n", cfg.ShadowLogFile)
		}
	}

	// Create escalator
	escalator := patrol.NewEscalator(workDir, rigName)
	cfg.Escalator = escalator

	// Ensure state file exists
	if err := patrol.EnsureStateFile(workDir, rigName); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create state file: %v\n", err)
	}

	// Run patrol loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := patrol.RunPatrolLoop(ctx, cfg); err != nil {
		return fmt.Errorf("patrol loop error: %w", err)
	}

	return nil
}

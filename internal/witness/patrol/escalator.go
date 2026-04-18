// Package patrol provides the Go-based witness patrol loop.
package patrol

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/witness"
)

// EscalationType represents the type of escalation.
type EscalationType string

const (
	EscalationHelpTriage     EscalationType = "help-triage"
	EscalationDirtyState    EscalationType = "dirty-state"
	EscalationRefineryStuck  EscalationType = "refinery-stuck"
	EscalationUnknown       EscalationType = "unknown"
	EscalationTimerGate     EscalationType = "timer-gate"
	EscalationSwarmComplete EscalationType = "swarm-complete"
)

// Escalation represents a case that cannot be resolved by the patrol loop.
type Escalation struct {
	Type        EscalationType `json:"type"`
	Polecat     string         `json:"polecat,omitempty"`
	Rig         string         `json:"rig"`
	Details     string         `json:"details"`
	RawData     map[string]any `json:"raw_data,omitempty"`
	Severity    string         `json:"severity"` // "critical", "high", "medium"
	Timestamp   time.Time      `json:"timestamp"`
	IsSecurity  bool           `json:"is_security"` // Critical security/vulnerability flag
}

// Escalator handles escalation routing and dispatch.
type Escalator struct {
	WorkDir string
	RigName string
}

// NewEscalator creates a new escalator.
func NewEscalator(workDir, rigName string) *Escalator {
	return &Escalator{
		WorkDir: workDir,
		RigName: rigName,
	}
}

// Escalate executes an escalation by creating a wisp and slinging to a dog.
func (e *Escalator) Escalate(esc *Escalation, dogName string) error {
	// Build labels
	labels := fmt.Sprintf("witness-escalation,rig:%s", esc.Rig)
	if esc.Polecat != "" {
		labels += fmt.Sprintf(",polecat:%s", esc.Polecat)
	}
	labels += fmt.Sprintf(",severity:%s", esc.Severity)

	// Build description
	var desc strings.Builder
	desc.WriteString(fmt.Sprintf("Witness escalation: %s\n\n", esc.Type))
	desc.WriteString(fmt.Sprintf("Details:\n%s\n\n", esc.Details))
	desc.WriteString(fmt.Sprintf("Rig: %s\n", esc.Rig))
	if esc.Polecat != "" {
		desc.WriteString(fmt.Sprintf("Polecat: %s\n", esc.Polecat))
	}
	desc.WriteString(fmt.Sprintf("Severity: %s\n", esc.Severity))
	desc.WriteString(fmt.Sprintf("Timestamp: %s\n\n", esc.Timestamp.Format(time.RFC3339)))

	if len(esc.RawData) > 0 {
		dataJSON, _ := json.MarshalIndent(esc.RawData, "", "  ")
		desc.WriteString("Raw data:\n")
		desc.WriteString(string(dataJSON))
	}

	// Create ephemeral wisp
	wispID, err := e.createWisp(esc, labels, desc.String())
	if err != nil {
		return fmt.Errorf("creating escalation wisp: %w", err)
	}

	// Sling to dog
	if err := e.slingToDog(wispID, dogName); err != nil {
		return fmt.Errorf("slinging to dog: %w", err)
	}

	return nil
}

func (e *Escalator) createWisp(esc *Escalation, labels, description string) (string, error) {
	args := []string{
		"create",
		"--ephemeral",
		"--wisp-type=patrol",
		"--title=" + fmt.Sprintf("witness-escalation:%s", esc.Type),
		"--description=" + description,
		"--label=" + labels,
		"--json",
	}

	output, err := util.ExecWithOutput(e.WorkDir, "bd", args...)
	if err != nil {
		return "", fmt.Errorf("bd create: %w\nOutput: %s", err, string(output))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", fmt.Errorf("parsing bd create JSON: %w\nOutput: %s", err, string(output))
	}

	return result.ID, nil
}

func (e *Escalator) slingToDog(wispID, dogName string) error {
	args := []string{"sling", wispID, e.RigName, "--dog", dogName}
	cmd := exec.Command("gt", args...)
	cmd.Dir = e.WorkDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gt sling: %w", err)
	}
	return nil
}

// RouteHelpMessage routes HELP messages based on keyword matching.
func RouteHelpMessage(msg *mail.Message) string {
	body := strings.ToLower(msg.Subject + " " + msg.Body)

	// Critical: security issues
	if strings.Contains(body, "security") || strings.Contains(body, "vulnerability") ||
		strings.Contains(body, "breach") || strings.Contains(body, "data corruption") ||
		strings.Contains(body, "data loss") {
		return "overseer"
	}

	// High: failures
	if strings.Contains(body, "crash") || strings.Contains(body, "panic") ||
		strings.Contains(body, "fatal") || strings.Contains(body, "oom") ||
		strings.Contains(body, "disk full") || strings.Contains(body, "connection refused") ||
		strings.Contains(body, "database error") {
		return "deacon"
	}

	// High: blocked
	if strings.Contains(body, "blocked") || strings.Contains(body, "merge conflict") ||
		strings.Contains(body, "deadlock") || strings.Contains(body, "stuck") ||
		strings.Contains(body, "cannot proceed") {
		return "mayor"
	}

	// Medium: decisions
	if strings.Contains(body, "which approach") || strings.Contains(body, "ambiguous") ||
		strings.Contains(body, "unclear") || strings.Contains(body, "design choice") ||
		strings.Contains(body, "architecture") {
		return "deacon"
	}

	// Medium: lifecycle
	if strings.Contains(body, "session") || strings.Contains(body, "respawn") ||
		strings.Contains(body, "zombie") || strings.Contains(body, "hung") ||
		strings.Contains(body, "timeout") || strings.Contains(body, "no progress") {
		return "witness"
	}

	// Default
	return "deacon"
}

// RouteDirtyState determines the appropriate dog for dirty state handling.
func RouteDirtyState(polecatName, dirtyType string) string {
	// Use investigate dog for general dirty state
	return "investigate"
}

// SeverityFor returns the severity for an escalation type.
// Pass isSecurity=true for security/vulnerability escalations to get "critical" severity.
func SeverityFor(t EscalationType, isSecurity bool) string {
	// Security/vulnerability escalations are always critical
	if isSecurity {
		return "critical"
	}

	switch t {
	case EscalationHelpTriage:
		return "medium"
	case EscalationDirtyState:
		return "medium"
	case EscalationRefineryStuck:
		return "high"
	case EscalationTimerGate:
		return "high"
	case EscalationSwarmComplete:
		return "low"
	default:
		return "medium"
	}
}

// EscalateHelpMessage escalates a HELP message to the appropriate handler.
func (e *Escalator) EscalateHelpMessage(msg *mail.Message) error {
	target := RouteHelpMessage(msg)

	// Check if this is a security/vulnerability message for severity
	isSecurity := isSecurityMessage(msg.Subject + " " + msg.Body)

	esc := &Escalation{
		Type:       EscalationHelpTriage,
		Rig:        e.RigName,
		Details:    fmt.Sprintf("HELP message: %s\n\n%s", msg.Subject, msg.Body),
		Severity:   SeverityFor(EscalationHelpTriage, isSecurity),
		Timestamp:  time.Now(),
		IsSecurity: isSecurity,
		RawData: map[string]any{
			"subject": msg.Subject,
			"from":    msg.From,
			"target":  target,
		},
	}

	return e.Escalate(esc, target)
}

// isSecurityMessage returns true if the message contains security keywords.
func isSecurityMessage(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "security") ||
		strings.Contains(lower, "vulnerability") ||
		strings.Contains(lower, "breach") ||
		strings.Contains(lower, "data corruption") ||
		strings.Contains(lower, "data loss")
}

// EscalateRefineryStuck escalates a stuck refinery to the appropriate handler.
func (e *Escalator) EscalateRefineryStuck(queueDepth int, stuckMinutes int) error {
	severity := "medium"
	if stuckMinutes > 30 {
		severity = "high"
	}

	esc := &Escalation{
		Type:    EscalationRefineryStuck,
		Rig:     e.RigName,
		Details: fmt.Sprintf("Refinery stuck for %d minutes with %d MRs in queue", stuckMinutes, queueDepth),
		Severity:  severity,
		Timestamp: time.Now(),
		RawData: map[string]any{
			"queue_depth":     queueDepth,
			"stuck_minutes":   stuckMinutes,
		},
	}

	return e.Escalate(esc, "investigate")
}

// EscalateTimerGate escalates an expired timer gate.
func (e *Escalator) EscalateTimerGate(gateID, gateType, description string) error {
	esc := &Escalation{
		Type:    EscalationTimerGate,
		Rig:     e.RigName,
		Details: fmt.Sprintf("Timer gate expired: %s (%s)\n%s", gateID, gateType, description),
		Severity:  "high",
		Timestamp: time.Now(),
		RawData: map[string]any{
			"gate_id":   gateID,
			"gate_type": gateType,
		},
	}

	return e.Escalate(esc, "investigate")
}

// RouteScanFindings routes scan findings to appropriate handlers.
// Returns escalations that need to be slung to dogs.
func (e *Escalator) RouteScanFindings(result *ScanResult, cfg PatrolConfig) ([]*Escalation, error) {
	var escalations []*Escalation

	// Route zombies
	for _, z := range result.Zombies {
		// Check if this zombie should be escalated based on respawn count
		if z.HookBead != "" && witness.ShouldBlockRespawn(cfg.WorkDir, z.HookBead) {
			// This bead has been respawned too many times — escalate to mayor
			esc := &Escalation{
				Type:    EscalationDirtyState,
				Polecat: z.Polecat,
				Rig:     cfg.Rig,
				Details: fmt.Sprintf("Zombie polecat %s exceeded respawn threshold (hook=%s)",
					z.Polecat, z.HookBead),
				Severity:  "high",
				Timestamp: time.Now(),
				RawData: map[string]any{
					"classification":   z.Classification,
					"agent_state":      z.AgentState,
					"hook_bead":        z.HookBead,
					"cleanup_status":   z.CleanupStatus,
					"action":           z.Action,
					"respawn_exceeded": true,
				},
			}
			escalations = append(escalations, esc)
		} else if z.CleanupStatus == "dirty" || z.CleanupStatus == "pending" {
			// Zombie with dirty state → escalate to deacon dog
			esc := &Escalation{
				Type:    EscalationDirtyState,
				Polecat: z.Polecat,
				Rig:     cfg.Rig,
				Details: fmt.Sprintf("Zombie polecat %s with dirty state: %s (hook=%s, action=%s)",
					z.Polecat, z.Classification, z.HookBead, z.Action),
				Severity:  "medium",
				Timestamp: time.Now(),
				RawData: map[string]any{
					"classification":  z.Classification,
					"agent_state":     z.AgentState,
					"hook_bead":       z.HookBead,
					"cleanup_status":  z.CleanupStatus,
					"action":          z.Action,
				},
			}
			escalations = append(escalations, esc)
		}
	}

	// Route stalls
	for _, s := range result.Stalls {
		esc := &Escalation{
			Type:    EscalationUnknown,
			Polecat: s.Polecat,
			Rig:     cfg.Rig,
			Details: fmt.Sprintf("Stalled polecat %s: %s (action=%s)",
				s.Polecat, s.StallType, s.Action),
			Severity:  "medium",
			Timestamp: time.Now(),
			RawData: map[string]any{
				"stall_type": s.StallType,
				"action":    s.Action,
			},
		}
		escalations = append(escalations, esc)
	}

	return escalations, nil
}

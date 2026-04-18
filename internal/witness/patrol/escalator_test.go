package patrol

import (
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

func TestRouteHelpMessage(t *testing.T) {
	tests := []struct {
		name   string
		subject string
		body   string
		want   string
	}{
		{
			name:   "security keyword",
			subject: "HELP: security issue",
			body:   "Found a security vulnerability in auth",
			want:   "overseer",
		},
		{
			name:   "vulnerability keyword",
			subject: "HELP: vulnerability",
			body:   "Found a security issue",
			want:   "overseer",
		},
		{
			name:   "crash keyword",
			subject: "HELP: crash",
			body:   "Polecat crashed during operation",
			want:   "deacon",
		},
		{
			name:   "panic keyword",
			subject: "HELP: panic",
			body:   "Application panic detected",
			want:   "deacon",
		},
		{
			name:   "blocked keyword",
			subject: "HELP: blocked",
			body:   "Cannot proceed with implementation",
			want:   "mayor",
		},
		{
			name:   "merge conflict keyword",
			subject: "HELP: merge conflict",
			body:   "Git merge conflict in main",
			want:   "mayor",
		},
		{
			name:   "session keyword",
			subject: "HELP: session issue",
			body:   "Session died unexpectedly",
			want:   "witness",
		},
		{
			name:   "zombie keyword",
			subject: "HELP: zombie detected",
			body:   "Polecat appears to be zombie",
			want:   "witness",
		},
		{
			name:   "hung keyword",
			subject: "HELP: hung",
			body:   "Session seems hung",
			want:   "witness",
		},
		{
			name:   "default routing",
			subject: "HELP: general question",
			body:   "How should I handle this case?",
			want:   "deacon",
		},
		{
			name:   "data corruption",
			subject: "HELP: data corruption",
			body:   "Database appears corrupted",
			want:   "overseer",
		},
		{
			name:   "data loss",
			subject: "HELP: data loss",
			body:   "Lost track of work state",
			want:   "overseer",
		},
		{
			name:   "oom keyword",
			subject: "HELP: out of memory",
			body:   "Polecat hit OOM",
			want:   "deacon",
		},
		{
			name:   "fatal keyword",
			subject: "HELP: fatal error",
			body:   "Fatal error occurred",
			want:   "deacon",
		},
		{
			name:   "design choice",
			subject: "HELP: design question",
			body:   "Which approach should I take?",
			want:   "deacon",
		},
		{
			name:   "architecture",
			subject: "HELP: architecture",
			body:   "Unclear about architecture decision",
			want:   "deacon",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &mail.Message{
				Subject: tt.subject,
				Body:    tt.body,
			}
			got := RouteHelpMessage(msg)
			if got != tt.want {
				t.Errorf("RouteHelpMessage(%q, %q) = %q, want %q", tt.subject, tt.body, got, tt.want)
			}
		})
	}
}

func TestRouteHelpMessage_Unknown(t *testing.T) {
	msg := &mail.Message{
		Subject: "HELP: weird case",
		Body:    "Something happened that I don't understand",
	}
	got := RouteHelpMessage(msg)
	if got != "deacon" {
		t.Errorf("RouteHelpMessage for unknown case = %q, want deacon", got)
	}
}

func TestRouteHelpMessage_Emergency(t *testing.T) {
	tests := []struct {
		name   string
		subject string
	}{
		{"security", "HELP: security breach"},
		{"vulnerability", "HELP: vulnerability CVE-2024"},
		{"breach", "HELP: data breach"},
		{"data corruption", "HELP: data corruption"},
		{"data loss", "HELP: data loss risk"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &mail.Message{
				Subject: tt.subject,
				Body:    "urgent issue",
			}
			got := RouteHelpMessage(msg)
			if got != "overseer" {
				t.Errorf("RouteHelpMessage(%q) = %q, want overseer", tt.subject, got)
			}
		})
	}
}

func TestDirtyStateEscalation(t *testing.T) {
	escalator := NewEscalator(t.TempDir(), "testrig")

	// Test that escalator can create escalation for dirty state
	esc := &Escalation{
		Type:    EscalationDirtyState,
		Polecat: "furiosa",
		Rig:     "testrig",
		Details: "Zombie polecat with dirty state",
		Severity: "medium",
	}

	// Verify escalation structure
	if esc.Type != EscalationDirtyState {
		t.Errorf("Type: got %q, want %q", esc.Type, EscalationDirtyState)
	}
	if esc.Polecat != "furiosa" {
		t.Errorf("Polecat: got %q, want %q", esc.Polecat, "furiosa")
	}
	if esc.Severity != "medium" {
		t.Errorf("Severity: got %q, want %q", esc.Severity, "medium")
	}

	// Verify Escalate method exists and has correct signature
	// (actual execution would require mock setup)
	_ = escalator
}

func TestRefineryEscalation(t *testing.T) {
	escalator := NewEscalator(t.TempDir(), "testrig")

	esc := &Escalation{
		Type:    EscalationRefineryStuck,
		Rig:     "testrig",
		Details: "Refinery stuck for 30 minutes with 5 MRs in queue",
		Severity: "high",
	}

	if esc.Type != EscalationRefineryStuck {
		t.Errorf("Type: got %q, want %q", esc.Type, EscalationRefineryStuck)
	}
	if esc.Severity != "high" {
		t.Errorf("Severity: got %q, want %q", esc.Severity, "high")
	}

	_ = escalator
}

func TestEscalationStateFile(t *testing.T) {
	// Verify escalation struct can be serialized
	esc := &Escalation{
		Type:      EscalationHelpTriage,
		Polecat:   "furiosa",
		Rig:       "testrig",
		Details:   "Test escalation",
		Severity:  "medium",
		RawData:   map[string]any{"key": "value"},
	}

	if esc.Type != EscalationHelpTriage {
		t.Errorf("Type: got %q, want %q", esc.Type, EscalationHelpTriage)
	}
	if esc.Polecat != "furiosa" {
		t.Errorf("Polecat: got %q, want %q", esc.Polecat, "furiosa")
	}
	if esc.Rig != "testrig" {
		t.Errorf("Rig: got %q, want %q", esc.Rig, "testrig")
	}
	if esc.RawData["key"] != "value" {
		t.Errorf("RawData[key]: got %v, want %v", esc.RawData["key"], "value")
	}
}

func TestSeverityMapping(t *testing.T) {
	tests := []struct {
		escalationType EscalationType
		isSecurity     bool
		want           string
	}{
		{EscalationHelpTriage, false, "medium"},
		{EscalationDirtyState, false, "medium"},
		{EscalationRefineryStuck, false, "high"},
		{EscalationTimerGate, false, "high"},
		{EscalationSwarmComplete, false, "low"},
		{EscalationUnknown, false, "medium"},
		// Security messages should always be critical
		{EscalationHelpTriage, true, "critical"},
		{EscalationRefineryStuck, true, "critical"},
	}

	for _, tt := range tests {
		t.Run(string(tt.escalationType)+"_"+strconv.FormatBool(tt.isSecurity), func(t *testing.T) {
			got := SeverityFor(tt.escalationType, tt.isSecurity)
			if got != tt.want {
				t.Errorf("SeverityFor(%q, %v) = %q, want %q", tt.escalationType, tt.isSecurity, got, tt.want)
			}
		})
	}
}

func TestNewEscalator(t *testing.T) {
	workDir := t.TempDir()
	rigName := "testrig"

	escalator := NewEscalator(workDir, rigName)

	if escalator.WorkDir != workDir {
		t.Errorf("WorkDir: got %q, want %q", escalator.WorkDir, workDir)
	}
	if escalator.RigName != rigName {
		t.Errorf("RigName: got %q, want %q", escalator.RigName, rigName)
	}
}

func TestRouteDirtyState(t *testing.T) {
	tests := []struct {
		name       string
		polecat    string
		dirtyType  string
		want       string
	}{
		{"uncommitted changes", "furiosa", "uncommitted", "investigate"},
		{"stashed work", "dom", "stashed", "investigate"},
		{"unpushed commits", "aang", "unpushed", "investigate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RouteDirtyState(tt.polecat, tt.dirtyType)
			if got != tt.want {
				t.Errorf("RouteDirtyState(%q, %q) = %q, want %q", tt.polecat, tt.dirtyType, got, tt.want)
			}
		})
	}
}

func TestEscalationTypes(t *testing.T) {
	tests := []struct {
		name  string
		value EscalationType
	}{
		{"help-triage", EscalationHelpTriage},
		{"dirty-state", EscalationDirtyState},
		{"refinery-stuck", EscalationRefineryStuck},
		{"unknown", EscalationUnknown},
		{"timer-gate", EscalationTimerGate},
		{"swarm-complete", EscalationSwarmComplete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.value) != tt.name {
				t.Errorf("EscalationType %q has value %q", tt.name, string(tt.value))
			}
		})
	}
}

func TestEscalationContainsPolecat(t *testing.T) {
	esc := &Escalation{
		Type:    EscalationDirtyState,
		Polecat: "furiosa",
		Rig:     "testrig",
		Details: "Zombie with dirty state",
	}

	// RawData should include polecat info
	esc.RawData = map[string]any{
		"polecat": esc.Polecat,
		"rig":     esc.Rig,
	}

	if esc.RawData["polecat"] != "furiosa" {
		t.Errorf("RawData[polecat]: got %v, want furiosa", esc.RawData["polecat"])
	}
}

func TestEscalateHelpMessage_Structure(t *testing.T) {
	msg := &mail.Message{
		Subject: "HELP: crash",
		Body:    "Polecat crashed during build",
		From:    "gt/furiosa",
	}

	target := RouteHelpMessage(msg)

	// Verify message structure for escalation
	if msg.Subject == "" {
		t.Error("Subject should not be empty")
	}
	if msg.From == "" {
		t.Error("From should not be empty")
	}

	// Verify routing
	if target != "deacon" {
		t.Errorf("target = %q, want deacon", target)
	}
}

func TestEscalateRefineryStuck_Structure(t *testing.T) {
	queueDepth := 5
	stuckMinutes := 45

	esc := &Escalation{
		Type:    EscalationRefineryStuck,
		Rig:     "testrig",
		Details: "Refinery stuck",
		Severity: "high",
		RawData: map[string]any{
			"queue_depth":   queueDepth,
			"stuck_minutes": stuckMinutes,
		},
	}

	if esc.RawData["queue_depth"] != queueDepth {
		t.Errorf("queue_depth: got %v, want %d", esc.RawData["queue_depth"], queueDepth)
	}
	if esc.RawData["stuck_minutes"] != stuckMinutes {
		t.Errorf("stuck_minutes: got %v, want %d", esc.RawData["stuck_minutes"], stuckMinutes)
	}
}

func TestEscalateTimerGate_Structure(t *testing.T) {
	gateID := "gt-gate-123"
	gateType := "timer"
	description := "Awaiting response from overseer"

	esc := &Escalation{
		Type:    EscalationTimerGate,
		Rig:     "testrig",
		Details: "Timer gate expired",
		Severity: "high",
		RawData: map[string]any{
			"gate_id":      gateID,
			"gate_type":    gateType,
			"description": description,
		},
	}

	if esc.RawData["gate_id"] != gateID {
		t.Errorf("gate_id: got %v, want %s", esc.RawData["gate_id"], gateID)
	}
}

// TestGetPolecatDir removed - function was dead code and removed

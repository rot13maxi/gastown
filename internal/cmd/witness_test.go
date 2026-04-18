package cmd

import (
	"strings"
	"testing"
)

func TestWitnessRestartAgentFlag(t *testing.T) {
	flag := witnessRestartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected witness restart to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestWitnessStartAgentFlag(t *testing.T) {
	flag := witnessStartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected witness start to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}
func TestWitnessPatrolShadowFlags(t *testing.T) {
	shadowFlag := witnessPatrolCmd.Flags().Lookup("shadow")
	if shadowFlag == nil {
		t.Fatal("expected witness patrol to define --shadow flag")
	}
	if shadowFlag.DefValue != "false" {
		t.Errorf("expected default --shadow to be 'false', got %q", shadowFlag.DefValue)
	}
	if !strings.Contains(shadowFlag.Usage, "Shadow mode") {
		t.Errorf("expected --shadow usage to mention 'Shadow mode', got %q", shadowFlag.Usage)
	}

	shadowLogFlag := witnessPatrolCmd.Flags().Lookup("shadow-log")
	if shadowLogFlag == nil {
		t.Fatal("expected witness patrol to define --shadow-log flag")
	}
	if shadowLogFlag.DefValue != "" {
		t.Errorf("expected default --shadow-log to be empty, got %q", shadowLogFlag.DefValue)
	}
	if !strings.Contains(shadowLogFlag.Usage, "shadow") {
		t.Errorf("expected --shadow-log usage to mention 'shadow', got %q", shadowLogFlag.Usage)
	}
}

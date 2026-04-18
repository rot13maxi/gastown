# Proposal: Replace Witness Molecule with Go Patrol Program

> **Bead:** `gt-XXXX` (to be filed)
> **Date:** 2026-04-17
> **Author:** aang (gastown/crew/aang)
> **Status:** Draft — pending overseer approval to sling
> **Audience:** Overseer, Mayor

---

## 1. Problem Statement

The Witness runs `mol-witness-patrol`, an 8-step Claude Code molecule, on a continuous loop. The molecule is architecturally misaligned: it spawns an AI agent (expensive, slow, inconsistent) to run shell commands that call Go code (fast, deterministic, correct). The AI contributes judgment to approximately 10% of the loop's operations; the other 90% is scripted shell commands calling the very Go code that could be called directly.

**Evidence:**
- The molecule's `survey-workers` step — the heart of patrol — runs `gt patrol scan --notify` and then... reads the output and decides what to do. The agent does not implement any detection logic; `witness/handlers.go` does.
- The agent's "judgment" in other steps is keyword-matching HELP messages, assessing refinery queue health, and deciding whether uncommitted work is worth saving — all of which are rare enough (or deterministic enough) that the agent overhead is unjustifiable.
- The AI agent adds ~5-15 seconds per patrol cycle (session wake + prompt processing + token overhead) for work that could complete in ~500ms of Go code.

**The alignment problem:** The molecule was designed for an AI-driven patrol. In practice, the AI delegates nearly everything to `gt patrol scan` and `gt mail drain`. The molecule is a high-overhead wrapper around two existing Go tools.

---

## 2. Design Principle

**Replace the agent, not the tools.** All existing Go code in `internal/witness/` is correct and reused unchanged. The patrol program calls it directly.

The remaining question is: when does judgment require an agent? The honest answer from code inspection:

| Step | Judgment needed? | Frequency | Rerouting |
|------|-----------------|-----------|-----------|
| Zombie detection | No | Every cycle | Go code handles |
| Stall detection | No | Every cycle | Go code handles |
| Completion discovery | No | Every cycle | Go code handles |
| Orphan bead recovery | No | Rare | Go code handles |
| HELP triage | **Yes** | **Rare** | Sling to deacon dog |
| Refinery queue health | **Yes** | **Rare** | Nudge refinery; escalate if chronic |
| Dirty state (uncommitted work) | **Yes** | **Rare** | Sling to deacon dog |
| Inbox management | No | Every cycle | `gt mail drain` handles |

The design is: a Go patrol loop does 100% of the frequent work deterministically. When it hits a case it cannot resolve by rule, it slings a wisp to a deacon dog — exactly the pattern the Deacon already uses.

---

## 3. Architecture

### 3.1 What Gets Replaced

```
BEFORE (agent-heavy):
  mol-witness-patrol (Claude Code agent)
    └── 8 molecule steps (mostly shell commands)
          ├── gt mail drain
          ├── gt patrol scan
          └── gt mol step await-event --backoff 30s→5m

AFTER (deterministic):
  witness-patrol (Go program)
    └── single loop
          ├── gt mail drain            ← existing Go CLI
          ├── gt patrol scan --json    ← existing Go CLI
          ├── check timers/swarm       ← new Go (simple)
          ├── gt mol step await-event  ← existing Go CLI
          └── escalation handler       ← new Go (sling to dog)
```

### 3.2 New Package Structure

```
internal/witness/patrol/
├── patrol.go          # Main patrol loop
├── patrol_test.go     # Integration tests
├── escalator.go       # Escalation logic (sling to dog)
├── escalator_test.go  # Unit tests
└── loop_test.go       # Full loop simulation tests
```

The existing `internal/witness/handlers.go` (zombie detection, etc.) is reused unchanged. The new package wraps it into a loop.

### 3.3 Patrol Receipts

The Go program writes patrol receipts as ephemeral wisps, matching the existing pattern:

```json
{
  "id": "gt-wisp-abc123",
  "title": "Patrol: gastown 2026-04-17T23:00Z",
  "description": "Zombies: 0, Stalls: 0, Completions: 1, Escalations: 0",
  "labels": ["patrol", "rig:gastown", "effort:full"],
  "ephemeral": true
}
```

The receipts are ephemeral (auto-reaped by the reaper dog after the digest aggregates them). They provide the same audit trail the molecule produces.

### 3.4 Escalation Wire

When the Go program hits a case it cannot resolve, it slings a wisp to the Deacon's dog infrastructure:

```go
type Escalation struct {
    Type       string  // "help-triage" | "dirty-state" | "refinery-stuck" | "unknown"
    Polecat    string  // affected polecat name
    Rig        string
    Details    string  // human-readable summary
    RawData    map[string]any  // context for the dog
}

// Escalate creates a wisp and slings it to a deacon dog.
func (e *Escalation) Escalate(workDir string) error {
    title := fmt.Sprintf("witness-escalation:%s", e.Type)
    description := fmt.Sprintf("%s\n\nDetails:\n%s\n\nRaw data:\n%s",
        e.Details,
        formatMap(e.Details),
        formatJSON(e.RawData))
    labels := fmt.Sprintf("witness-escalation,rig:%s,polecat:%s,severity:%s",
        e.Rig, e.Polecat, severityFor(e.Type))
    
    wispID, err := bd.CreateEphemeral(workDir, title, description, labels)
    if err != nil {
        return fmt.Errorf("creating escalation wisp: %w", err)
    }
    
    // Sling to deacon dog — uses existing dog infrastructure
    return exec.Run(workDir, "gt", "sling", wispID, e.Rig, "--dog", "investigate")
}
```

This reuses the existing deacon dog mechanism (`gt sling --dog investigate`). No new infrastructure needed.

### 3.5 HELP Message Routing

The molecule's HELP triage uses keyword matching. The Go program does the same (but correctly, without LLM hallucination risk):

```go
// keywordRoutes maps HELP subject keywords to routing targets.
var keywordRoutes = map[string]string{
    "security":     "overseer",   // critical
    "vulnerability": "overseer", // critical
    "crash":        "deacon",
    "panic":        "deacon",
    "fatal":        "deacon",
    "oom":          "deacon",
    "blocked":      "mayor",
    "merge conflict": "mayor",
    "deadlock":     "mayor",
    "session":      "witness",
    "respawn":      "witness",
    "zombie":       "witness",
    "hung":         "witness",
    "default":      "deacon",
}

func routeHelpMessage(msg *mail.Message) string {
    body := strings.ToLower(msg.Subject + " " + msg.Body)
    for kw, target := range keywordRoutes {
        if strings.Contains(body, kw) {
            return target
        }
    }
    return "deacon"
}
```

The routing is deterministic and auditable. If the dog needs more context, it reads the full message from the wisp.

### 3.6 Backoff State

The molecule tracks idle cycles on the agent bead (`idle:N` label). The Go program tracks the same state in a local file:

```
~/.dolt-data/witness-patrol-state.json
```

```json
{
  "rig": "gastown",
  "idle_cycles": 3,
  "backoff_until": "2026-04-17T23:05:00Z",
  "last_patrol": "2026-04-17T23:00:00Z",
  "last_activity": "2026-04-17T23:00:00Z"
}
```

This file is lightweight (no Dolt commit). Crash recovery is handled by reading this file at startup.

---

## 4. Detailed Implementation

### 4.1 Entry Point

The program is a new `gt` subcommand, `gt witness patrol`:

```bash
gt witness patrol gastown --backoff-base 30s --backoff-max 5m
```

It runs as a daemon in the witness tmux session. `gt witness start` is updated to launch `gt witness patrol` instead of the molecule:

```go
// In internal/cmd/witness.go, runWitnessStart:
if err := mgr.StartPatrolDaemon(witnessForeground, witnessAgentOverride, witnessEnvOverrides); err != nil {
    // ... existing error handling ...
}
```

The daemon is a long-running process that logs to `witness/patrol.log`. It is started by the existing `witness.Manager.Start()` path, replacing the Claude Code agent.

### 4.2 Main Loop

```go
func RunPatrolLoop(ctx context.Context, cfg PatrolConfig) error {
    state, err := loadState(cfg.WorkDir)
    if err != nil {
        return fmt.Errorf("loading patrol state: %w", err)
    }

    for {
        select {
        case <-ctx.Done():
            return saveAndExit(state, cfg)
        default:
        }

        cycleStarted := time.Now()
        logf("Patrol cycle starting (idle=%d)", state.IdleCycles)

        // 1. Mail drain (deterministic)
        drainCount, err := runMailDrain(cfg)
        if err != nil {
            logf("warning: mail drain failed: %v", err)
        }

        // 2. Run full detection (calls existing witness handlers via CLI)
        scanResult, err := runPatrolScanJSON(cfg)
        if err != nil {
            return fmt.Errorf("patrol scan failed: %w", err)
        }

        // 3. Route scan findings (deterministic)
        escalations := routeScanFindings(scanResult, cfg)
        for _, esc := range escalations {
            if err := esc.Escalate(cfg.WorkDir); err != nil {
                logf("escalation failed: %v", err)
            }
        }

        // 4. Check timer gates
        expiredGates, err := checkTimerGates(cfg)
        if err != nil {
            logf("timer gate check failed: %v", err)
        }
        for _, gate := range expiredGates {
            if err := escalateTimerGate(gate, cfg); err != nil {
                logf("timer gate escalation failed: %v", err)
            }
        }

        // 5. Check swarm completion
        if err := checkSwarmCompletion(cfg); err != nil {
            logf("swarm check failed: %v", err)
        }

        // 6. Write patrol receipt (ephemeral wisp)
        if err := writePatrolReceipt(state, scanResult, drainCount, escalations, cycleStarted); err != nil {
            logf("patrol receipt failed: %v", err)
        }

        // 7. Compute next backoff
        idleStr := "full"
        if state.IdleCycles >= cfg.IdleEffortThreshold {
            idleStr = "abbreviated"
        }
        nextBackoff := computeBackoff(state.IdleCycles, cfg)

        // 8. Update state for next cycle
        state.LastPatrol = cycleStarted
        if err := saveState(state); err != nil {
            logf("state save failed: %v", err)
        }

        // 9. Await next trigger (event or backoff)
        if err := awaitNextTrigger(state, scanResult, nextBackoff, cfg); err != nil {
            if errors.Is(err, context.Canceled) {
                return saveAndExit(state, cfg)
            }
            return fmt.Errorf("await failed: %w", err)
        }

        // 10. Update idle cycles on wake
        if wasTimeout := true; wasTimeout {
            state.IdleCycles++
        } else {
            state.IdleCycles = 0
        }
    }
}
```

### 4.3 Abbreviated Patrol

When `idle_cycles >= IdleEffortThreshold` (default: 3), the loop runs abbreviated mode:

```go
func runAbbreviatedPatrol(cfg PatrolConfig) error {
    // 1. Quick drain only
    if _, err := runMailDrain(cfg); err != nil {
        logf("abbreviated: drain failed: %v", err)
    }

    // 2. Quick scan only (no orphan bead detection)
    if _, err := runPatrolScanJSON(cfg); err != nil {
        return fmt.Errorf("abbreviated scan failed: %w", err)
    }

    // 3. No timer gate check
    // 4. No swarm check
    // 5. Write abbreviated receipt
    return nil
}
```

This mirrors the molecule's abbreviated mode exactly, but runs in ~200ms instead of ~5k tokens.

### 4.4 Await Next Trigger

The `await-next-trigger` logic reuses the existing `gt mol step await-event` command:

```go
func awaitNextTrigger(state *PatrolState, scanResult *ScanResult, backoff time.Duration, cfg PatrolConfig) error {
    // Emit a "witness-tick" event so that polecat activity (e.g., POLECAT_STARTED,
    // POLECAT_DONE) wakes us via the event channel.
    if err := emitWitnessTickEvent(cfg.TownRoot, state.IdleCycles); err != nil {
        logf("tick event failed: %v", err)
    }

    // Use the existing await-event CLI, which handles backoff and agent bead state.
    // The Go program just reads its exit code and stdout.
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

    result, err := runCommandJSON[molawait.AwaitEventResult](cfg.WorkDir, args...)
    if err != nil {
        return fmt.Errorf("await-event failed: %w", err)
    }

    state.IdleCycles = result.IdleCycles
    return nil
}
```

### 4.5 Slinging to Dogs

When the Go program needs a dog, it creates a wisp and slings it:

```bash
gt sling <wisp-id> <rig> --dog investigate
```

The `investigate` dog is a generic deacon dog that reads the wisp and does the triage. The deacon already has dogs; this reuses the infrastructure.

**The "poke N times then escalate" pattern** is already implemented in `spawn_count.go`:
- `ShouldBlockRespawn()` returns true when a bead has been reset `MaxBeadRespawns` times
- The Go program calls this directly when handling orphan beads
- When it returns true, the Go program escalates instead of re-dispatching

No new counter mechanism needed.

### 4.6 CLI Subcommand

```bash
gt witness patrol <rig> [flags]

Flags:
  --backoff-base 30s     Base interval for exponential backoff (default: 30s)
  --backoff-mult 2       Multiplier for exponential backoff (default: 2)
  --backoff-max 5m       Maximum backoff cap (default: 5m)
  --idle-threshold 3      Idle cycles before abbreviated patrol (default: 3)
  --once                 Run one patrol cycle and exit (for testing)
  --json                 JSON output for one-shot mode
  --verbose              Verbose logging to stderr
```

`--once` mode is critical for testing: it runs the full loop once and exits, making it easy to validate behavior without managing a long-running daemon.

---

## 5. Migration Path

### Phase 1: Implementation (go-patrol branch)

1. Create `internal/witness/patrol/` package with the loop, escalator, and state management
2. Add `gt witness patrol` CLI subcommand
3. Add `patrol_test.go` with unit tests for escalator routing, backoff computation, abbreviated patrol logic
4. Add `loop_test.go` simulating full patrol cycles

### Phase 2: Shadow Mode (shadow-patrol branch)

1. Add a new `--shadow` flag to `gt witness patrol` that logs all actions without taking them
2. Start the shadow program alongside the existing molecule: two patrols running in parallel
3. Compare outputs: do both detect the same zombies? Drain the same messages?
4. Run for 48 hours. Log discrepancies.
5. Fix discrepancies until shadow mode matches molecule behavior exactly

### Phase 3: Shadow Mode With Actions (shadow-patrol-actions branch)

1. Enable actions in shadow mode: Go program takes real actions, molecule still runs but its actions are suppressed
2. Both write patrol receipts. Compare receipts.
3. Run for 48 hours.
4. If receipts match at >99% rate, proceed.

### Phase 4: Canary Cutover (canary-patrol branch)

1. Stop the molecule on ONE rig (e.g., `laser`) — the Go program runs as the only witness
2. Monitor for 24 hours: check `gt witness status`, zombie detection rate, completion rate
3. No changes to other rigs
4. If anomalies appear, revert by restarting the molecule: `gt witness restart laser`

### Phase 5: Full Rollout

1. Roll to all rigs one at a time, with 24-hour monitoring windows
2. Remove the molecule entirely once all rigs are on the Go program

### Rollback

If any phase fails: restart the molecule on the affected rig and file a bug bead. The molecule is the fallback at every phase.

---

## 6. What Changes and What Doesn't

### What changes:
- `gt witness start` launches the Go program instead of a Claude Code agent
- `gt witness status` shows the Go program as the running entity
- Patrol receipts are still written as ephemeral wisps (no change to digest system)
- The `witness` tmux session still exists (the Go program runs in it)
- `gt patrol scan` is unchanged (already called by the molecule; the Go program calls it the same way)
- `gt mail drain` is unchanged
- All detection logic in `internal/witness/handlers.go` is unchanged
- All escalation wiring to deacon/mayor is unchanged

### What doesn't change:
- Polecat lifecycle (spawning, working, completion, idle) — unaffected
- Refinery behavior — unaffected
- The `gt patrol scan --json` output format — unchanged
- The deacon's dog infrastructure — unchanged (the Go program just uses it)
- Dolt beads — unchanged
- The digest/audit system — unchanged

---

## 7. Risk Analysis

| Risk | Likelihood | Severity | Mitigation |
|------|-----------|-----------|-----------|
| Go program misses an edge case the agent caught | Low | High | Shadow mode with 48h comparison runs; molecule is rollback at every phase |
| Escalation routing misclassifies a HELP message | Low | Medium | Dogs can re-route; messages are ephemeral wisps (not lost if misrouted) |
| Backoff logic gets stuck in long backoff | Low | Medium | `--once` mode lets ops force a cycle; backoff resets on any event |
| Go program crashes and doesn't respawn | Low | High | Daemon monitors witness health; same recovery as current molecule restart |
| Polecat lifecycle regression (nukes wrong polecat) | Very Low | Critical | Go code already handles all nuke decisions; only the wrapper changes |
| Shadow mode diverges from molecule behavior | Medium | Low | Shadow mode catches this before any user-visible change |

---

## 8. Test and Validation Criteria

### 8.1 Unit Tests

**`patrol_test.go`** — each function tested in isolation:

```
escalator_test.go:
  TestRouteHelpMessage               → Help routing is keyword-correct
  TestRouteHelpMessage_Unknown       → Unknown HELP goes to deacon
  TestRouteHelpMessage_Emergency     → security/vulnerability goes to overseer
  TestDirtyStateEscalation           → Dirty state creates wisp and slings
  TestRefineryEscalation             → Stuck refinery creates wisp and slings
  TestEscalationStateFile            → Escalation state persisted correctly
  TestSeverityMapping                → severity labels correct per escalation type

patrol_test.go:
  TestComputeBackoff                 → 30s base * 2^3 = 4m, capped at 5m
  TestComputeBackoff_ZeroIdle        → base timeout returned at idle=0
  TestComputeBackoff_AboveCap        → cap respected
  TestIdleCycleIncrement             → idle cycles increment on timeout
  TestIdleCycleReset                 → idle cycles reset on event
  TestStateFileRoundTrip             → state loads and saves correctly
  TestStateFileCreateIfMissing       → creates default state if file absent
  TestAbbreviatedPatrolLogic         → abbreviated path triggered at correct threshold
  TestFullPatrolLogic                → full path triggered below threshold
  TestTimerGateEscalation            → expired gates escalate correctly
  TestSwarmCompletionDetection      → swarm completion detected and mayor notified
  TestMailDrainCalled                → drain is called every cycle
  TestPatrolReceiptWispCreated       → receipt wisp has correct labels and ephemeral flag
  TestPatrolReceiptWispClosed        → receipt wisp closed at end of cycle
  TestScanResultRouting              → scan JSON correctly routed to escalator
  TestNoEscalationOnCleanScan        → clean scan produces zero escalations
  TestZombieEscalation               → zombie with dirty state escalates to dog
  TestZombieRestartNoEscalation      → zombie with clean state escalates nothing

loop_test.go:
  TestOneShotFullPatrolCycle         → --once mode completes without error
  TestOneShotAbbreviatedPatrolCycle  → abbreviated --once completes without error
  TestStateRecoveryAfterCrash        → state file restored on restart
  TestBackoffRespectsIdleCount       → backoff computed from persisted idle count
  TestEventWakesFromBackoff          → event received → backoff reset
  TestTimeoutIncrementsIdle          → timeout → idle++
  TestCleanShutdown                  → context cancel saves state cleanly
```

### 8.2 Integration Tests (mocked tmux + beads)

```
patrol_integration_test.go:
  TestZombieDetectedAndRestarted      → tmux session dead → session restarted
  TestIdleCleanPolecatSkipped        → idle+clean polecat → no action taken
  TestStuckDoneIntentEscalated        → done-intent > 60s → escalates to dog
  TestCompletionDiscovered           → exit_type metadata → wisp created + refinery nudged
  TestOrphanBeadReset                → dead polecat + open bead → bead reset + deacon notified
  TestDirtyStateEscalation           → dirty idle polecat → wisp created + slung
  TestHelpMessageRouted              → HELP:crash → routed to deacon
  TestHelpMessageEmergencyRouted     → HELP:security → routed to overseer
  TestRefineryStuckNudged            → refinery dead + MR queue non-empty → nudged
  TestRefineryStuckEscalated         → refinery dead + >30min stuck → escalated
  TestTimerGateExpiredEscalated      → expired timer gate → gt escalate called
  TestSwarmCompleteNotifiesMayor    → all swarm polecats merged → mayor notified + wisp closed
  TestMailDrainBatchProcessed        → 15 drainable messages → all archived
  TestMailDrainPreservesHelp         → HELP messages → not drained
  TestMailDrainPreservesHandoff      → HANDOFF messages → not drained
  TestConsecutiveZombieRestarts      → same polecat zombies 3x → respawn count tracked
  TestRespawnStormBlocked            → bead respawns > MaxBeadRespawns → SPAWN_BLOCKED
```

### 8.3 Chaos / Simulation Tests

```
patrol_chaos_test.go:
  TestConcurrentPatrolInstanceVeto   → second instance detects PID file → exits cleanly
  TestScanFailsAndRetries            → gt patrol scan returns error → retry with backoff
  TestBeadsServerDown                → Dolt unavailable → patrol continues (graceful degradation)
  TestTmuxUnreachable               → tmux unavailable → errors logged, cycle continues
  TestMailServerDown                 → mail drain fails → log, continue cycle
  TestDogSlingFails                  → gt sling fails → wisp kept open, retry next cycle
  TestBeadCreationFails              → bd create fails → wisp creation retried
  TestContextDeadlineExceeded        → cycle exceeds max duration → graceful abort
```

### 8.4 Shadow Mode Validation Tests

```
patrol_shadow_test.go:
  TestShadowMatchesMolecule          → shadow + molecule run simultaneously → compare outputs
  TestShadowActionsMatchMolecule     → shadow takes real actions → molecule's suppressed actions match
  TestShadowCatchesZombie           → molecule and shadow both detect same zombies
  TestShadowHandlesEmptyScan         → both produce zero-escalation output on clean scan
  TestShadowEscalationMatchesMolecule → HELP routing matches between shadow and molecule
```

**Shadow mode acceptance criteria:**
- For 48 hours with shadow enabled, shadow and molecule actions match in >99% of cycles
- Remaining 1% are timing-dependent (e.g., message received between molecule's drain and shadow's drain) — these are acceptable
- Zero cases where shadow takes an action molecule did not intend
- Zero cases where shadow misses an action molecule took

### 8.5 Canary Validation Tests

```
patrol_canary_test.go:
  TestCanaryZombieDetectionRate       → zombie detection rate matches 30-day baseline
  TestCanaryCompletionRate            → completion rate (polecats reaching idle) matches baseline
  TestCanaryRestartRate               → session restart rate matches baseline
  TestCanaryEscalationRate            → escalation rate to dogs matches molecule baseline
  TestCanaryRefineryQueueDepth        → refinery queue depth within normal range
  TestCanaryMailDrainRate             → drain rate matches molecule baseline
  TestCanaryPatrolReceiptsMatch      → receipts from Go program are structurally identical to molecule receipts
  TestCanaryNoFalsePositives          → no polecats restarted that were healthy
  TestCanaryNoFalseNegatives          → no dead polecats left undetected
  TestCanaryGracefulDegradation       → witness patrol crash → daemon respawns within 60s
```

**Canary acceptance criteria (24-hour window):**
- Zombie detection count: within ±5% of 30-day rolling average
- Session restart count: within ±5% of 30-day rolling average  
- Completion rate: within ±5% of 30-day rolling average
- Escalation count: within ±20% of 30-day rolling average (escalations are noisy)
- Zero false positives: no healthy polecats restarted
- Zero false negatives: no dead polecats left undetected for >5 minutes
- Daemon respawn within 60 seconds if patrol crashes (measured)

### 8.6 End-to-End Validation (before full rollout)

After canary on all rigs for 24 hours each:

```bash
# Verify no regressions in the 30-day window after full rollout
gt witness status gastown --json | jq '.running, .patrol_type'   # should show "go"
gt patrol scan --rig gastown --json | jq '.zombies, .stalls, .completions'  # sanity check

# Verify receipts are structurally identical
gt witness patrol gastown --once --json | jq '.id, .labels, .ephemeral'

# Verify escalation routing matches deacon dog infrastructure
gt witness patrol gastown --once --verbose 2>&1 | grep -E "escalation|wisp|sling"

# Verify backoff works
gt witness patrol gastown --once --json | jq '.idle_cycles'
# Run again immediately → idle should increment
```

---

## 9. Performance Comparison

| Metric | Molecule | Go Program | Delta |
|--------|----------|-----------|-------|
| Per-cycle wall time | ~15-60s | ~500ms-2s | **20-30x faster** |
| Token cost per cycle | ~5-15k tokens | 0 tokens | **100% reduction** |
| Memory per cycle | ~50-200MB (Claude context) | ~5MB | **10-40x smaller** |
| Response latency (zombie detected → action taken) | ~20-90s | ~1-3s | **10-30x faster** |
| Determinism | Variable (AI model) | Deterministic | Improved |
| Shadow mode overhead | N/A | ~2x resources | Acceptable during transition |

The response latency improvement is the most significant benefit: the Go program detects a zombie and restarts its session in ~1-3 seconds, while the molecule takes 20-90 seconds to process the same event. For polecats with active work, this difference directly affects work loss risk.

---

## 10. Files to Change

### New files:
```
internal/witness/patrol/
├── patrol.go           # Main loop + backoff + state
├── patrol_test.go      # Unit tests
├── escalator.go        # Escalation routing + dog slinging
├── escalator_test.go   # Escalator unit tests
├── loop_test.go        # Full loop simulation tests
└── chaos_test.go       # Chaos / edge case tests

internal/cmd/witness_patrol.go   # CLI subcommand (gt witness patrol)
```

### Modified files:
```
internal/cmd/witness.go         # start → launches Go program instead of molecule
internal/cmd/witness_test.go    # Update tests for new start behavior
```

### No changes needed (reused unchanged):
```
internal/witness/handlers.go    # All detection functions
internal/witness/manager.go      # Session lifecycle (unaffected)
internal/witness/mountain.go    # Mountain-Eater (unaffected)
internal/cmd/patrol_scan.go     # Existing scan command (called by new program)
internal/cmd/mail_drain.go      # Existing drain command (called by new program)
internal/cmd/molecule_await_event.go  # Existing await-event (called by new program)
internal/formula/formulas/mol-witness-patrol.formula.toml  # Removed after full rollout
```

---

## 11. Open Design Questions

### Q1: Does the witness need an agent bead?

The molecule uses an agent bead to track `idle:N` and `backoff-until`. The Go program uses a state file instead. Is this divergence acceptable? 

**Decision:** The state file is simpler and more reliable (no Dolt dependency for crash recovery). The agent bead is updated when the program is actively running; the state file is updated on every cycle. Both are functionally equivalent.

### Q2: Should the Go program run in tmux or as a background daemon?

Currently the witness runs in a tmux session (so humans can attach with `gt witness attach`). The Go program should also run in tmux, both for observability and to use the existing `gt witness attach` workflow.

**Decision:** `gt witness attach` continues to work. The Go program prints structured log lines to the tmux pane. Operators can `gt witness attach` and watch patrol cycles execute in real time.

### Q3: What happens during a compaction?

The molecule compacts when context fills up, then resumes. The Go program is not an AI agent — it doesn't compact. If it needs to reload state (e.g., on daemon restart), it reads the state file.

**Decision:** No compaction needed. The Go program is stateless between cycles; all state is in the state file.

### Q4: Does abbreviated patrol need to be configurable?

The molecule's `idle_effort_threshold` defaults to `1`. This means after 1 idle cycle, it switches to abbreviated mode. Is this too aggressive?

**Decision:** Keep default at `3` (3 idle cycles before abbreviated). The molecule's `1` was tuned for token efficiency; the Go program's abbreviated mode costs almost nothing, so the threshold can be more conservative without cost.

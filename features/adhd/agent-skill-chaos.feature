@adhd
@z0-physical
@domain-agent-skills
Feature: Agent Skill Tool Chain Chaos Resilience
  Agent skills chain multiple tool calls together with dependencies.
  Each tool's output feeds into the next tool's input. Chaos testing
  validates resilience under tool failures, timeouts, and state changes.

  Background:
    Given an agent skill that performs a multi-step task
    And the skill calls tools via MCP in sequence
    And tools may have dependencies on previous tool results

  # ── simple tool chains ─────────────────────────────────────────────────────

  Scenario: agent skill chains tools successfully
    Given skill: "Check cluster health"
    When the skill executes:
      1. Call adhd.isotope.instance → get isotope ID
      2. Call adhd.status with isotope ID → get health counts
      3. Call adhd.lights.list → enumerate all lights
      4. Filter lights by status=red
      5. Return red light count
    Then all 4 tool calls succeed in order
    And the final result is accurate
    And each tool receives correct input from previous result

  Scenario: agent skill handles tool output transformation
    Given skill: "Summarize cluster state"
    When the skill executes:
      1. Call adhd.status → {lights: {...}, cluster_peers: [...]}
      2. Transform: extract cluster_peers list
      3. Call adhd.isotope.peers → get detailed peer info
      4. Correlate peers with lights
      5. Return enriched peer list
    Then output transformation doesn't lose data
    And correlation between peers and lights is correct

  # ── tool failure resilience ────────────────────────────────────────────────

  Scenario: skill retries failed tool call
    Given skill: "Get cluster status" with retry_count=3
    When adhd.status is called:
      - Attempt 1: timeout (1000ms)
      - Attempt 2: error (malformed response)
      - Attempt 3: success
    Then skill retries automatically
    And final result is correct after 3 attempts
    And skill logs all retry attempts

  Scenario: skill aborts if tool fails permanently
    Given skill: "Get peer list"
    When adhd.isotope.peers is called:
      - Attempt 1: error (peer unreachable)
      - Attempt 2: error (same peer still unreachable)
      - Attempt 3: timeout
    And max_retries=3 is reached
    Then skill aborts with descriptive error
    And error message includes: "could not reach isotope.peers after 3 attempts"
    And skill does NOT proceed to dependent tools

  # ── concurrent agent skills ────────────────────────────────────────────────

  Scenario: multiple agents call tools simultaneously
    Given 5 agent skills running concurrently
    When each skill chains: status → lights.list → lights.get("light-1")
    Then all 5 skills complete successfully
    And no tool calls are dropped or corrupted
    And response ordering is maintained per skill
    And cross-skill interference is zero

  Scenario: concurrent skills modifying shared state
    Given skill A: "Add light and verify"
    And skill B: "List all lights and count"
    When both skills run concurrently:
      - Skill A: calls adhd.lights.add, then adhd.lights.list
      - Skill B: calls adhd.lights.list, then adhd.lights.count
    Then state changes from A are visible to B
    And counts are consistent (not stale)
    And no race conditions occur

  # ── timeout and latency chaos ────────────────────────────────────────────

  Scenario: skill handles slow tool responses
    Given skill with timeout_per_tool=500ms
    When tool A responds in 200ms, tool B responds in 400ms
    Then skill completes successfully (sum=600ms but per-tool <500ms)
    And total_time = sum of individual timeouts

  Scenario: skill fails gracefully on cumulative timeout
    Given skill with total_timeout=1000ms
    When tool chain executes:
      1. Tool A: 300ms
      2. Tool B: 300ms
      3. Tool C: 300ms (cumulative=900ms)
      4. Tool D: 300ms (cumulative=1200ms > timeout)
    Then skill aborts after tool C succeeds
    And tool D is not called
    And error indicates: "exceeded total timeout of 1000ms after 3/4 tools"

  # ── state consistency during tool chains ────────────────────────────────

  Scenario: skill reads consistent state across tool chain
    Given skill: "Verify light state"
    When skill calls:
      1. adhd.lights.get("light-1") → status=green
      2. Some external process changes light to red
      3. adhd.lights.get("light-1") → status=red
      4. adhd.status → includes count of red lights
    Then skill sees the state change reflected in step 3 and 4
    And no stale data is cached between tool calls

  Scenario: tool side effects are visible to subsequent tools
    Given skill: "Change light status and report"
    When skill calls:
      1. adhd.lights.update("light-1", status="red")
      2. adhd.status → reads current counts
      3. Verify light-1 contributes to red count
    Then update in step 1 is reflected in step 2's counts
    And no race condition between write and read

  # ── error propagation ─────────────────────────────────────────────────────

  Scenario: skill propagates errors with context
    Given skill calling tool A, then tool B
    When tool A fails with error: "isotope not found"
    Then skill:
      - Catches the error
      - Adds context: "Failed at step 1/3: adhd.isotope.instance"
      - Does NOT call tool B
      - Returns error with full context chain

  Scenario: skill distinguishes transient vs permanent failures
    When tool returns:
      - timeout → transient (retry)
      - 429 rate limit → transient (backoff + retry)
      - 404 not found → permanent (abort)
      - 503 service unavailable → transient (retry)
    Then skill applies correct retry strategy for each error type

  # ── agent skill cancellation ──────────────────────────────────────────────

  Scenario: skill can be cancelled mid-chain
    Given skill executing: [tool A, tool B, tool C, tool D]
    And tool A completes, tool B is in progress
    When cancel() is called
    Then:
      - Tool B is interrupted (if cancellable)
      - Tool C and D are not called
      - Resources are cleaned up
      - Error indicates: "skill cancelled after step 2/4"

  # ── chaos injection in tool chains ─────────────────────────────────────────

  Scenario: chaos test injects failures at each step
    When running skill with chaos injection:
      - Run 1: fail tool A
      - Run 2: fail tool B
      - Run 3: fail tool C
      - Run 4: timeout tool B
      - Run 5: all succeed
    Then skill handles each failure correctly
    And test reports which steps are resilient vs fragile

  Scenario: chaos test detects missing error handling
    When tool chain has no error handling:
      1. Call adhd.status
      2. Call adhd.lights.list (depends on status)
      3. Process results
    And tool #1 fails
    Then:
      - Without error handling: skill crashes or returns garbage
      - Test FAILS: "error handling missing at step 2"
      - Indicates: tool B called with nil/invalid input from tool A

  # ── performance under chaos ────────────────────────────────────────────────

  Scenario: skill performance degrades gracefully
    When all tools respond with 100ms+ latency (vs 10ms baseline)
    Then:
      - Skill still completes successfully
      - Total time increases proportionally (~400ms for 4 tools @ 100ms)
      - No timeouts or failures due to latency alone

  Scenario: skill detects and reports performance regression
    When tool A baseline=10ms, now responds in 500ms
    Then:
      - Skill completes (if total timeout allows)
      - Skill logs: "Tool adhd.isotope.instance slower than expected (500ms vs 10ms)"
      - Performance regression is detected and can be alerted on

  # ── regression: broken tool chains ─────────────────────────────────────────

  Scenario: chaos test detects missing tool in chain
    When a tool referenced by skill is removed (e.g., adhd.lights.get)
    And skill still calls it
    Then:
      - Tool returns 404 or method-not-found
      - Skill fails at that step
      - Test FAILS: "expected tool not found in MCP server"

  Scenario: chaos test detects incompatible tool output
    When tool B expects output format from tool A
    And tool A's output format changes
    Then:
      - Step 1 succeeds
      - Step 2 fails to parse tool A's output
      - Test FAILS: "tool chain incompatibility at step 2"

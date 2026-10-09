---
name: adhd-gherkin-patterns
description: Use when writing, reviewing, extending, or editing Gherkin .feature
  files under features/adhd/ in the ADHD project — adding scenarios, naming a
  new feature file, choosing @ tags, or documenting blocked/incomplete (-42i)
  certification. Trigger on requests like 'add a feature', 'write a scenario',
  'new .feature', 'document this behavior as Gherkin', 'which tags should this
  use', or when a change needs an acceptance test in the ADHD suite. Carries the
  project's tag taxonomy (@adhd, @z0-physical..@z3-epistemic, @domain-*), the
  rationale-first Feature description style, Background rules, the
  table/evidence idioms, and the green/red/refactor and chaos/negative
  conventions. Do NOT use for generic Cucumber step-definition code or non-ADHD
  projects.
---
# ADHD Gherkin feature patterns

`features/adhd/*.feature` is the ADHD acceptance suite — it runs with
`go run ./cmd/adhd test --features=features/adhd/` and doubles as living
documentation. When you add or extend a feature, match the conventions below so
it reads like the rest of the suite.

## 1. Header: tag block, then `Feature:`

Open every file with tags, then the `Feature:` line.

```gherkin
@adhd
@z1-temporal
@domain-mcp-server
Feature: ADHD Certified Feature Browser — MCP Tools for Live Coverage
```

- `@adhd` — always, and first. Marks the file as part of this suite.
- Exactly one layer tag: `@z0-physical`, `@z1-temporal`, `@z2-relational`, or
  `@z3-epistemic`. Pick the *highest* layer the feature genuinely reaches — concrete
  state (z0), change over time (z1), coordination between actors (z2), or
  proof/trust/knowledge (z3). Most files are `@z0-physical`; only reach higher when
  the behavior is really about evolution, relationships, or evidence.
- Exactly one `@domain-<domain>` tag naming the capability under test:
  `@domain-dashboard`, `@domain-lights`, `@domain-certification`, `@domain-discovery`,
  `@domain-mcp-server`, `@domain-registry-health`, `@domain-proxy`, … Reuse an existing
  domain before inventing one — search the suite for `@domain-`.
- Optional cross-cutting tags: `@continuous-monitoring`, `@negative-test`,
  `@capability-discovery`, `@demo`, `@rung-42i`.

One tag per line or space-separated on one line are both used; stay consistent
within a file. Scenario-level tags classify scenarios *inside* a file
(`@lifecycle @start`, `@green`, `@red`, `@refactor`, `@negative @detection`, `@phase-1`) — keep
the set coherent for the file. The full taxonomy is in
`references/pattern-catalog.md`.

## 2. The `Feature:` description explains WHY

The prose under `Feature:` is not a restatement of the steps. Write 2–6 short
paragraphs that state the intent, the failure the feature prevents, and any
model the reader needs (states, rungs, invariants). Lead with the point.

> Certification is not a one-time achievement. Endpoints must continuously pass
> smoke tests or their certification level is automatically downgraded.
>
> This prevents false confidence: a badge that means "currently healthy" not
> "was once tested."

When the feature depends on another file or an external component, record it as
a `# Dependencies:` comment — one file per line, naming the file and why it
matters.

## 3. `Background:` is shared environment only

23 of 29 files have a `Background:`. Use it only for setup every scenario needs —
normally the running system, not data. Keep per-scenario data in the scenario.

```gherkin
Background:
  Given ADHD is running and can reach the MCP endpoint
  And at least one smoke-alarm is reachable
```

## 4. Scenarios: plain, behavior-named, evidence-first

- Use `Scenario:` only. Do NOT use `Scenario Outline:` / `Examples:` — the suite has none.
  Express variation with explicit scenarios or a table inside a step.
- Name the scenario as a behavior, lowercase, no "Test that …":
  `certification degrades when tests start failing`,
  `caller at rung 2 additionally receives isotope_id and last_transit`.
- Order steps Given → When → Then; continue with `And`. `But` appears only a few times —
  prefer `And`.
- Assert observable evidence, never internals: response fields, dashboard text,
  light color/status, counts, error codes, latency bounds. State what must be
  *absent* too — privacy and negative assertions are common ("isotope_id is
  absent", "no payload information is recoverable", "no error is raised — the
  response is structurally valid").

Separate sections with a labelled rule comment:

```gherkin
  # ── trust rungs ────────────────────────────────
```

## 5. Tables, lists, and fenced blocks are first-class

Structured data lives inline in the feature, not in step-definition code:

- Field lists: `And each entry contains: feature_file, scenario_name, certified (bool), last_transit`
- Markdown tables for matrices, timelines, and reports:

```gherkin
  And we see:
    | Change | Alert | Severity | Action |
    | ✓ → ⚠ | Degraded warning | MEDIUM | Review logs |
    | ⚠ → ✗ | Critical failure | HIGH | Incident open |
```

- Bullet lists for enumerations (`- item`), often after a `:`.
- Fenced blocks (```json, ```) for literal wire format, policy, or rendered UI.

Prefer one scenario with a table over several near-identical scenarios.

## 6. Suite-specific conventions

**Rungs, isotopes, and -42i.** Authorization is modeled as trust rungs 0–4,
advanced by observing certified isotope transits. -42i ("42 incomplete") is the
*explicit* incomplete-certification state. Use the vocabulary exactly:
`isotope_id`, `isotope_classification`, transit, receipt, nonce, `42i distance`,
`base64url(SHA256(...))`. A scenario that moves a rung or distance must assert the
new value and the emitted `CapabilityVerifiedMsg`.

Identity is *identified*, never self-declared: ADHD presents an instance ID and
the smoke-alarm's registry decides the rung. Don't write scenarios where a
client asserts its own rung.

**Rung-gated fields.** To test authorization, write paired scenarios at
different rungs and assert which fields are present vs absent (e.g. rung 0 gets
`feature_file`; rung 2 adds `isotope_id`; `cluster_member` needs rung 3).

**Negative tests.** A feature about rejecting the wrong thing gets
`@negative-test`, and the description states the inversion: failing the negative
assertion *is* the success. Pair it with the positive path ("complete
certification requires both positive AND negative tests").

**Red / green / refactor.** When work is blocked by another component, tag
scenarios `@green` (passes today), `@red` (blocked), `@refactor` (transition path). On
each `@red` scenario add `# BLOCKED: <component> hasn't implemented this` and a
`# Why this matters:` note, then list the blocker in a `@refactor @blocker-list`
scenario with an owner column. This is how incomplete certification is surfaced
instead of hidden.

**Chaos and load.** Chaos scenarios state volume, concurrency, and a bound,
then assert no loss: `100 changes in 5 seconds`,
`20 SSE subscribers connect simultaneously`,
`response times remain under 500ms (p95)`, `no state is skipped or duplicated`.
Close with a regression scenario that would fail if the guarantee were removed
("the test would fail if aggregation wasn't thread-safe").

**Demo.** `@demo` scenarios run the full system against a local cluster (`-demo`)
and assert demo/production parity ("differ only in isotope type, not in code
paths").

## 7. Keep counts honest

The suite documents as well as tests, so numbers in prose are load-bearing. Do
not restate a total that can drift (e.g. "40 feature files", "16 tools") inside
a scenario; either derive it ("all loaded feature files") or update it in the
same change that changes the count. A stale count is a false assertion.

## Checklist before you commit

- Header has `@adhd`, exactly one `@z*`, exactly one `@domain-*`.
- Description explains *why*, with dependencies in `#` comments.
- `Background` contains environment only.
- Plain `Scenario:`, behavior-named, Given/When/Then with observable,
  evidence-based `Then`s.
- Structured data uses tables / lists / fenced blocks.
- Blocked work is `@red` with `# BLOCKED` and `# Why this matters`; -42i where
  certification is incomplete.
- No drifting counts.
- `go run ./cmd/adhd test --features=features/adhd/` passes.

For the full tag catalog, copy-paste templates (lifecycle, chaos, negative,
red/green/refactor, JSON-RPC), and a vocabulary glossary, read
`references/pattern-catalog.md`.

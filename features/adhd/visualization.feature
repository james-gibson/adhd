@adhd
@z0-physical
@domain-dashboard
Feature: ADHD Visualization — lipgloss-Based Rich Dashboard Layout
  ADHD uses lipgloss to render a multi-panel dashboard that consolidates
  cluster health, Gherkin feature certification, skill certification, and
  honeypot status into a single navigable view. Browsing features is
  treated as a first-class activity alongside verification and monitoring.

  # lipgloss provides: styled strings, box layouts, borders, color profiles.
  # The dashboard is split into panels navigable by keyboard.
  # All panels are renderable from the same model state — View() remains pure.
  #
  # Panel layout (default):
  #   ┌──────────────────────────────────────────────────────────┐
  #   │  ADHD  [rung: 3 chaos-certified]  [42i: 38]  [demo]     │  ← header
  #   ├────────────────────┬─────────────────────────────────────┤
  #   │  Cluster Lights    │  Feature Browser                    │  ← main panels
  #   │  (left)            │  (right)                            │
  #   ├────────────────────┴─────────────────────────────────────┤
  #   │  Details / Skill Browser / Honeypot Status              │  ← detail panel
  #   └──────────────────────────────────────────────────────────┘

  Background:
    Given ADHD is running with lipgloss rendering enabled
    And the terminal supports at least 80 columns and 24 rows

  # ── header bar ──────────────────────────────────────────────────────────────

  Scenario: header shows ADHD's current trust rung with its named level
    Given ADHD is certified at rung 3 by the smoke-alarm
    When the header is rendered
    Then it shows "rung: 3 chaos-certified" in a styled badge
    And the badge colour reflects the rung level:
      | rung | colour  |
      | 0    | dim     |
      | 1    | white   |
      | 2    | cyan    |
      | 3    | green   |
      | 4    | blue    |

  Scenario: header shows the current 42i distance as a compact meter
    Given ADHD's 42i distance is 38
    When the header is rendered
    Then it shows "42i: 38" with a short horizontal bar indicating distance within the rung ceiling
    And the bar fills left-to-right: empty = max distance, full = zero distance

  Scenario: header indicates demo mode with a distinct label
    Given ADHD is running in demo mode (plaintext isotopes)
    When the header is rendered
    Then it shows a "[demo]" badge in amber/yellow
    And the badge is absent in production mode

  # ── cluster lights panel (left) ──────────────────────────────────────────────

  Scenario: cluster lights are grouped by source with lipgloss border boxes
    Given the cluster has lights from "alarm-a" and "alarm-b"
    When the cluster panel is rendered
    Then each source group is enclosed in a lipgloss border box
    And the group header shows the source name and a status summary badge
    And the status summary badge colour matches the worst-case light in the group

  Scenario: light status indicators use lipgloss-styled characters, not only emoji
    When a light is rendered
    Then the status indicator is a lipgloss-styled character:
      | status | character | style              |
      | green  | ●         | bold green         |
      | red    | ●         | bold red           |
      | yellow | ●         | bold yellow        |
      | dark   | ○         | dim                |
    And emoji are used as a fallback when the terminal does not support colour

  Scenario: selected light is highlighted with a lipgloss reverse-video style
    Given light "smoke:alarm-a/t1" is selected
    When the panel is rendered
    Then "smoke:alarm-a/t1" is rendered with lipgloss reverse-video (background/foreground swapped)
    And a "›" prefix (styled bold) appears before the selected light
    And unselected lights have a plain "  " prefix

  Scenario: honeypot nodes are rendered with a distinct indicator in the cluster panel
    Given "honeypot-alpha" is a honeypot node in the cluster
    When the cluster panel is rendered
    Then "honeypot-alpha" is shown with a "◈" indicator (honeypot symbol)
    And if clean: "◈" is styled dim green with label "clean"
    And if triggered: "◈" is styled bold red with label "contact!"
    And honeypot nodes are visually separated from health lights by a thin divider

  # ── feature browser panel (right) ────────────────────────────────────────────

  Scenario: the feature browser panel shows Gherkin features with certification status
    Given the feature browser is the active right panel
    When the panel is rendered
    Then each feature file is shown as a row with:
      | column       | content                              |
      | indicator    | ● green / ○ dark / ◑ stale-demo      |
      | feature_file | short name (e.g. "mdns-discovery")   |
      | rung badge   | "rung 2" or "exists" or "—"          |
    And the panel title is "Features [4/10 certified]"

  Scenario: browsing features is navigable independently of the cluster lights panel
    Given the feature browser panel is focused (tab or arrow key)
    When the user presses j/k (or down/up)
    Then the selected row in the feature panel moves
    And the cluster lights panel remains at its current selection
    And the detail panel below updates to show the selected feature's scenarios

  Scenario: the detail panel shows scenario-level coverage for the selected feature
    Given feature "adhd/mdns-discovery" is selected in the feature browser
    When the detail panel renders
    Then it lists each Gherkin scenario in that feature
    And each scenario shows its certification state:
      | state       | indicator |
      | certified   | ✓ green   |
      | uncovered   | · dim     |
      | lapsed      | ✗ yellow  |

  Scenario: the feature browser highlights which cluster member certified each scenario
    Given ADHD is at rung 3 and the feature "adhd/mdns-discovery" is certified by "alarm-a/t1"
    When the detail panel renders
    Then the certifying member "alarm-a/t1" is shown next to the scenario
    And it is styled in a subdued colour to indicate provenance without visual clutter

  # ── skill browser panel (detail panel, tab) ───────────────────────────────────

  Scenario: pressing tab in the detail panel cycles between Feature view and Skill view
    Given the detail panel is showing Feature coverage
    When the user presses tab
    Then the detail panel switches to Skill view
    And "Skill view" shows all skills with:
      | column         | content                        |
      | indicator      | rung-coloured ●                |
      | skill_name     | short name                     |
      | rung badge     | "exists" / "deterministic" / "scenario-linked" |
      | distance badge | "d:0" / "d:2" styled dimmer per hop |

  Scenario: a skill with variation failures shows a yellow warning badge
    Given "start-here" has 2 variation failures and 42i distance 16
    When the skill view renders "start-here"
    Then it shows a yellow "⚠ 2 var" badge
    And the 42i distance is shown as "42i:16" in dim text

  # ── honeypot status panel (detail panel, tab) ─────────────────────────────────

  Scenario: cycling tab shows a Honeypot status view
    Given the demo cluster has 1 honeypot node
    When the user tabs to the Honeypot view
    Then the detail panel shows a "Routing Integrity" summary:
      | row              | content                               |
      | Honeypots clean  | 1/1 ◈ green                           |
      | Last checked     | <timestamp>                           |
      | Mesh routing     | ✓ no unexpected contacts detected     |

  Scenario: a triggered honeypot shows a prominent alert in the Honeypot view
    Given "honeypot-alpha" received a detection event
    When the Honeypot view renders
    Then the panel border turns red
    And the detection summary is shown in bold red:
      "◈ honeypot-alpha — contact detected — <timestamp>"
    And the event details are shown: skill, routing trace, originating instance

  # ── panel navigation ─────────────────────────────────────────────────────────

  Scenario: panels are navigated by keyboard without a mouse
    Then the following key bindings apply:
      | key     | action                                   |
      | j / ↓   | move selection down in focused panel     |
      | k / ↑   | move selection up in focused panel       |
      | Tab     | cycle focus: Lights → Features → Skills → Honeypot → Lights |
      | Enter   | open detail for selected item            |
      | v       | trigger verify for selected feature (rung 3+ only) |
      | ?       | toggle help overlay                      |
      | q / Esc | quit                                     |

  Scenario: the help overlay renders all key bindings in a lipgloss popup box
    When the user presses "?"
    Then a centred lipgloss box appears over the dashboard
    And it lists all key bindings with descriptions
    And pressing any key dismisses it

  # ── responsive layout ────────────────────────────────────────────────────────

  Scenario: narrow terminal (< 100 cols) collapses to single-panel mode
    Given the terminal width is 72 columns
    When the dashboard renders
    Then only the active panel is shown (full width)
    And Tab cycles between: Lights, Features, Skills, Honeypot
    And the header shrinks to show only the most critical indicators

  Scenario: the layout reflows when the terminal is resized
    Given the dashboard is running in wide mode (two panels)
    When the terminal is resized to 72 columns
    Then the layout switches to single-panel mode without restart
    And the selected item and panel focus are preserved

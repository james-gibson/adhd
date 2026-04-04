@adhd
@z0-physical
@domain-dashboard
Feature: ADHD Dashboard Keyboard Operations
  Dashboard responds to keyboard input and user commands

  Scenario: Navigate lights with arrow keys
    Given a dashboard with 5 lights
    When I press the down arrow
    Then the selection moves to light 2
    When I press the down arrow again
    Then the selection moves to light 3
    When I press the up arrow
    Then the selection moves back to light 2

  Scenario: Navigate lights with vim keys
    Given a dashboard with 5 lights
    When I press 'j' (vim down)
    Then the selection moves forward
    When I press 'k' (vim up)
    Then the selection moves backward

  Scenario: Quit dashboard with 'q'
    Given a running dashboard
    When I press 'q'
    Then the dashboard exits cleanly
    And no error is raised

  Scenario: Quit dashboard with Ctrl+C
    Given a running dashboard
    When I press Ctrl+C
    Then the dashboard exits cleanly
    And the terminal is restored

  Scenario: Refresh selected light with 'r'
    Given a dashboard with a light selected
    When I press 'r'
    Then the selected light is refreshed
    And the status is updated

  Scenario: Show details with 's'
    Given a dashboard with a light selected
    When I press 's'
    Then a details view is displayed
    And the light's metadata is shown

  Scenario: Execute command with 'e'
    Given a dashboard with a light selected
    And the light has a linked command
    When I press 'e'
    Then the command is executed
    And the output is displayed

  Scenario: Selection bounds are enforced
    Given a dashboard with 3 lights
    When I press down arrow 5 times
    Then the selection stays at light 3
    And no error occurs
    When I press up arrow 5 times
    Then the selection stays at light 1
    And no error occurs

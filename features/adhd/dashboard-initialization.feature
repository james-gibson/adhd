@adhd
@z0-physical
@domain-dashboard
Feature: ADHD Dashboard Initialization
  Dashboard starts with proper state and light discovery

  Scenario: Dashboard initializes with empty lights
    Given a fresh dashboard instance
    When the dashboard is initialized
    Then no lights are displayed
    And the dashboard is ready for input

  Scenario: Dashboard discovers features from default paths
    Given a dashboard instance
    When I initialize the dashboard
    And features are present in the search paths
    Then each feature generates a light
    And all lights have status "dark" (unknown)

  Scenario: Dashboard displays lights in order
    Given a dashboard with 3 lights
    When the dashboard renders
    Then lights are displayed in order
    And navigation index starts at 0
    And the first light is selected

  Scenario: Dashboard handles missing feature paths gracefully
    Given a dashboard configured with non-existent paths
    When the dashboard initializes
    Then no error is raised
    And a warning is logged
    And the dashboard still functions

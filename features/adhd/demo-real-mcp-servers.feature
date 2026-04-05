@adhd
@z0-physical
@domain-certification
@demo
Feature: Demo — Testing Real MCP Servers from the Registry

  The official MCP Registry (https://registry.modelcontextprotocol.io/)
  lists 100+ MCP server implementations from the community.

  Our certification tests can verify which ones actually implement
  the MCP protocol correctly, and detect impostors.

  This demonstrates the practical value of positive + negative testing.

  Background:
    Given the official MCP registry is accessible
    And test infrastructure (HURL, curl, bash) is available

  # ──────────────────────────────────────────────────────────────
  # Reference Servers: Official implementations (Testable)
  # ──────────────────────────────────────────────────────────────

  @demo @official-reference
  Scenario: Official reference servers are MCP-certified
    Given the official MCP repository at github.com/modelcontextprotocol/servers
    And reference servers are cloned and running locally:
      | Server     | Port | Purpose                           |
      | fetch      | 3000 | Web content fetching              |
      | git        | 3001 | Repository tools                  |
      | filesystem | 3002 | Secure file operations            |
      | memory     | 3003 | Knowledge graph persistence       |
      | everything | 3004 | Reference/test server             |

    When we run the positive certification test:
      ```bash
      hurl --variable endpoint=http://localhost:3000 \
        tests/hurl/demo-real-mcp-servers.hurl
      ```

    Then all reference servers return:
      | Response | Meaning |
      | 200 OK   | ✓ MCP-compliant |
      | JSON-RPC structure | ✓ Correct format |
      | adhd.status works | ✓ Tool calls execute |

    And the certification status is: ✓ CERTIFIED

  @demo @official-reference
  Scenario: Detect if a reference server is misconfigured
    Given a reference server (e.g., fetch) is running but misconfigured
    When we run the positive test
    Then the test FAILS (status != 200 or missing JSON-RPC structure)
    And we correctly identify the misconfiguration

  # ──────────────────────────────────────────────────────────────
  # Service-Based Servers: Hosted services (Auth-dependent)
  # ──────────────────────────────────────────────────────────────

  @demo @service-based @pending
  Scenario: Service-based servers require credentials/setup
    Given MCP servers from the registry that are service-based:
      | Server      | Type              | Status      |
      | Notion      | Productivity      | Needs auth  |
      | MongoDB     | Database          | Needs host  |
      | GitHub      | Version control   | Needs token |
      | AWS         | Cloud platform    | Needs creds |
      | Salesforce  | CRM               | Enterprise  |
      | ClickHouse  | Data warehouse    | Needs setup |

    When we attempt to test these without setup
    Then tests are marked PENDING (not failed)
    And documentation explains how to enable them:
      ```bash
      # Example: GitHub MCP server
      export GITHUB_TOKEN="ghp_..."
      npm install @modelcontextprotocol/server-github
      node_modules/.bin/mcp-server-github &
      ```

    And once configured locally, certification can proceed

  @demo @service-based
  Scenario: Certified service-based server with credentials
    Given a service-based server (e.g., GitHub) is properly configured
    And environment variables (GITHUB_TOKEN) are set
    When we run the positive test against its local instance
    Then it passes certification (just like reference servers)

  # ──────────────────────────────────────────────────────────────
  # Community Servers: Ecosystem implementations
  # ──────────────────────────────────────────────────────────────

  @demo @community-servers
  Scenario: Discover and test community-contributed servers
    Given the MCP registry lists 100+ community servers:
      | Category | Examples |
      | Trading | Lona Trading, aTars (crypto), Financial Datasets |
      | Code Analysis | Semgrep, GitHub, JetBrains |
      | Infrastructure | Contabo, SpotDB, Chroma |
      | Productivity | Notion, Gmail, Slack |

    When developers contribute new MCP servers
    And register them in the official registry
    Then our certification tests can verify their compliance:
      - Is the published endpoint MCP-compliant? ✓/✗
      - Does it handle concurrent requests? ✓/✗
      - Does error recovery work? ✓/✗

    And the registry can display certification badges:
      | Badge | Meaning |
      | ✓ Certified | Passes full positive tests |
      | ⏳ Pending | Needs auth/setup |
      | ✗ Uncertified | Failed negative tests |

  # ──────────────────────────────────────────────────────────────
  # Negative Testing: Detecting Imposters
  # ──────────────────────────────────────────────────────────────

  @demo @negative @imposters
  Scenario: Detect endpoints claiming to be MCP but aren't
    Given websites that are NOT MCP servers:
      | URL | What It Actually Is |
      | example.com | Generic documentation domain |
      | google.com | Search engine |
      | github.com (homepage) | Git hosting website |
      | random-api.com | Some other JSON API |

    When we run the negative certification test:
      ```bash
      hurl --variable endpoint=https://example.com \
        tests/hurl/negative-endpoint.hurl
      ```

    Then they correctly FAIL certification:
      | Result | Status Code | Reason |
      | ✓ Rejected | 405, 404, 500, timeout | Not JSON-RPC MCP |
      | ✓ Detected | Not 200 OK | Wrong response |
      | ✓ Classified | Empty/HTML response | Wrong format |

    And we correctly avoid trusting them

  @demo @negative @false-positives
  Scenario: Prevent false positives (wrong service on right port)
    Given someone runs a different service on the MCP port (e.g., port 3000)
    And it happens to return HTTP 200
    When we run tests
    Then we distinguish:
      | Scenario | Test Result | Action |
      | Real MCP server | ✓ Pass positive test | Trust it |
      | Wrong service | ✓ Pass negative test | Reject it |
      | No service | ✓ Pass negative test | Mark dead |

    And the assertion checks JSON-RPC structure, not just status

  # ──────────────────────────────────────────────────────────────
  # Integration: Registry Validation Pipeline
  # ──────────────────────────────────────────────────────────────

  @demo @registry-validation
  Scenario: Automatically validate all registry entries
    Given the MCP registry contains 100+ servers
    When fire-marshal runs validation
    Then it:
      1. Queries the registry API (https://registry.modelcontextprotocol.io/v0.1/servers)
      2. For each server with a public endpoint:
         - Runs positive test
         - Runs negative test
         - Records certification level
      3. Generates a report:
         ```
         Total servers: 104
         Certified (-42i):     8  (core reference + some community)
         Pending auth:        48  (service-based, needs setup)
         Non-responsive:      40  (no public endpoint available)
         Uncertified:          8  (failed tests, mark for review)
         ```

    And the registry displays badges:
      ```
      ✓ fetch             Certified
      ✓ git               Certified
      ⏳ GitHub MCP server Pending (needs auth)
      ✓ example.com       Rejected (correctly detected as non-MCP)
      ```

  @demo @ci-cd
  Scenario: CI/CD gate for MCP servers
    Given a pull request that adds a new MCP server
    When the PR is opened
    Then CI/CD runs:
      ```
      1. Build the server
      2. Start it on localhost
      3. Run demo-real-mcp-servers.hurl
      4. Verify positive test passes
      5. Verify negative tests still work
      6. Report certification level
      ```

    And the PR gets a comment:
      ```
      ✓ MCP Certification Report

      Server: example-mcp
      Status: ✓ Certified (-42i)

      - adhd.status: ✓ PASS
      - Tool chaining: ✓ PASS
      - Error recovery: ✓ PASS
      - Receipt validation: ✗ BLOCKED (waiting on smoke-alarm)

      Ready to merge!
      ```

  # ──────────────────────────────────────────────────────────────
  # Practical: Running the Demo
  # ──────────────────────────────────────────────────────────────

  @demo @practical @runnable
  Scenario: Run the demo suite yourself
    When you execute:
      ```bash
      # 1. Query the live registry
      bash tests/hurl/test-mcp-registry.sh

      # 2. Clone official servers
      git clone https://github.com/modelcontextprotocol/servers.git
      cd servers && npm install

      # 3. Start a reference server
      node dist/fetch/index.js &

      # 4. Test it
      hurl --variable endpoint=http://localhost:3000 \
        tests/hurl/demo-real-mcp-servers.hurl

      # 5. Test a non-MCP (should fail/reject)
      hurl --variable endpoint=https://example.com \
        tests/hurl/negative-endpoint.hurl
      ```

    Then you see:
      ```
      Testing official fetch server:
        ✓ status == 200
        ✓ jsonpath("$.result") exists
        ✓ PASS (MCP-certified)

      Testing example.com:
        ✓ status != 200 (assertion passed, correctly rejected)
        ✓ PASS (negative test correct)
      ```

    And you understand:
      - How to certify real MCP servers
      - How to detect imposters
      - How to integrate with CI/CD
      - How registry validation works

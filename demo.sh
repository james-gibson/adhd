#!/bin/bash
# Demo launcher for ADHD with dashboard + headless auto-discovery

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
ADHD_BIN="./bin/adhd"
CONFIG="./adhd-example.yaml"
LOG_FILE="/tmp/adhd-headless.jsonl"
DASHBOARD_PORT="9090"
HEADLESS_LOG="/tmp/adhd-headless.jsonl"

# Track PIDs started by THIS script instance
MANAGED_PIDS=()

# Function to print colored output
info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[!]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check prerequisites
check_prerequisites() {
    info "Checking prerequisites..."

    if [ ! -f "$ADHD_BIN" ]; then
        error "ADHD binary not found: $ADHD_BIN"
        error "Run: go build -o ./bin/adhd ./cmd/adhd"
        exit 1
    fi
    success "ADHD binary found"

    if [ ! -f "$CONFIG" ]; then
        warn "Config file not found: $CONFIG"
        info "Using default config location"
    fi
    success "Prerequisites OK"
}

# Cleanup function - only kill PIDs managed by THIS script instance
cleanup() {
    # Only cleanup if we have managed PIDs (full_demo mode)
    if [ ${#MANAGED_PIDS[@]} -eq 0 ]; then
        return
    fi

    info "Cleaning up demo processes..."

    # Kill only the PIDs we explicitly started
    for pid in "${MANAGED_PIDS[@]}"; do
        if ps -p "$pid" > /dev/null 2>&1; then
            kill "$pid" 2>/dev/null || true
        fi
    done

    sleep 1
    success "Cleanup complete"
}

# Start dashboard
start_dashboard() {
    info "Starting ADHD Dashboard (Prime)..."
    info "Port: $DASHBOARD_PORT"
    info ""
    info "Controls:"
    info "  j/k     - Navigate"
    info "  s       - Show details"
    info "  r       - Refresh"
    info "  e       - Execute"
    info "  q       - Quit"
    info ""
    info "Press Ctrl+C to stop..."
    info ""

    # Run dashboard (will block)
    "$ADHD_BIN" --config "$CONFIG" --mcp-addr ":$DASHBOARD_PORT"
}

# Start headless
start_headless() {
    info "Starting ADHD Headless (Prime-Plus)..."
    info "Log file: $HEADLESS_LOG"
    info ""
    info "Headless will:"
    info "  1. Start MCP server on random port"
    info "  2. Auto-discover dashboard as prime"
    info "  3. Buffer and push logs every 5 seconds"
    info "  4. Log MCP traffic to JSONL"
    info ""
    info "Press Ctrl+C to stop..."
    info ""

    # Clean up old log
    rm -f "$HEADLESS_LOG"

    "$ADHD_BIN" --headless \
        --config "$CONFIG" \
        --mcp-addr :0 \
        --log "$HEADLESS_LOG" \
        --prime-plus \
        --prime-addr "http://localhost:$DASHBOARD_PORT/mcp" \
        --debug
}

# Monitor logs
monitor_logs() {
    info "Monitoring JSONL logs..."
    info "Press Ctrl+C to stop"
    echo ""

    # Wait for log file to be created
    while [ ! -f "$HEADLESS_LOG" ]; do
        sleep 0.5
    done

    # Watch log file grow
    tail -f "$HEADLESS_LOG" | while read line; do
        # Extract method from JSONL
        method=$(echo "$line" | jq -r '.method // "unknown"' 2>/dev/null)
        type=$(echo "$line" | jq -r '.type // ""' 2>/dev/null)

        if [ ! -z "$method" ] && [ ! -z "$type" ]; then
            echo "  $(date +%H:%M:%S) | $type | $method"
        fi
    done
}

# Main demo menu
main_menu() {
    echo ""
    echo -e "${BLUE}=== ADHD Demo Launcher ===${NC}"
    echo ""
    echo "Select mode:"
    echo "  1) Dashboard only (TUI)"
    echo "  2) Headless only (logging)"
    echo "  3) Full demo (Dashboard + Headless in separate terminals)"
    echo "  4) Monitor logs from headless"
    echo "  5) Query dashboard health"
    echo "  6) Exit"
    echo ""
    echo "Flags:"
    echo "  ./demo.sh N                 # Run mode N directly (no menu)"
    echo "  ./demo.sh 3 --manual        # Show commands instead of spawning terminals"
    echo ""
    read -p "Choice [1-6]: " choice

    handle_choice "$choice"
}

# Handle menu choice (can be called with or without argument)
handle_choice() {
    local choice=$1
    local flag=$2

    case $choice in
        1)
            start_dashboard
            ;;
        2)
            start_headless
            ;;
        3)
            full_demo "$choice" "$flag"
            ;;
        4)
            monitor_logs
            ;;
        5)
            query_health
            ;;
        6)
            info "Exiting..."
            cleanup
            exit 0
            ;;
        *)
            error "Invalid choice: $choice"
            main_menu
            ;;
    esac
}

# Full demo with separate terminals
full_demo() {
    # Check if --manual flag provided
    local manual_mode=false
    if [ "$2" == "--manual" ]; then
        manual_mode=true
    fi

    if [ "$manual_mode" = true ]; then
        info "Full demo - Manual mode (copy and paste commands in separate terminals)"
        info ""
        echo -e "${BLUE}=== Terminal 1: Dashboard ===${NC}"
        echo "$ADHD_BIN --config $CONFIG --mcp-addr :$DASHBOARD_PORT"
        echo ""
        echo -e "${BLUE}=== Terminal 2: Headless ===${NC}"
        echo "$ADHD_BIN --headless --config $CONFIG --mcp-addr :0 --log $HEADLESS_LOG --prime-plus --prime-addr http://localhost:$DASHBOARD_PORT/mcp --debug"
        echo ""
        echo -e "${BLUE}=== Terminal 3: Monitor Logs ===${NC}"
        echo "tail -f $HEADLESS_LOG | jq -c '{type, method, timestamp}'"
        echo ""
        info "Copy each command above into a separate terminal window and run it."
        return 0
    fi

    info "Starting full demo in separate terminals..."
    info ""
    info "Terminal 1 will show the dashboard"
    info "Terminal 2 will show headless logs"
    info "Terminal 3 will show JSONL logs"
    info ""

    # Terminal 1: Dashboard
    info "Opening Terminal 1 (Dashboard)..."
    if command -v open &> /dev/null; then
        # macOS - opens in native Terminal app
        open -a Terminal <<EOF
cd $(pwd)
$ADHD_BIN --config $CONFIG --mcp-addr :$DASHBOARD_PORT
EOF
    elif command -v x-terminal-emulator &> /dev/null; then
        # Linux
        x-terminal-emulator -e "$ADHD_BIN --config $CONFIG --mcp-addr :$DASHBOARD_PORT" &
        MANAGED_PIDS+=($!)
    else
        warn "Could not open new terminal. Starting dashboard in background..."
        "$ADHD_BIN" --config "$CONFIG" --mcp-addr ":$DASHBOARD_PORT" &
        MANAGED_PIDS+=($!)
    fi

    # Wait for dashboard to start
    sleep 2

    # Terminal 2: Headless
    info "Opening Terminal 2 (Headless)..."
    if command -v open &> /dev/null; then
        # macOS - opens in native Terminal app
        open -a Terminal <<EOF
cd $(pwd)
$ADHD_BIN --headless --config $CONFIG --mcp-addr :0 --log $HEADLESS_LOG --prime-plus --prime-addr http://localhost:$DASHBOARD_PORT/mcp --debug
EOF
    elif command -v x-terminal-emulator &> /dev/null; then
        # Linux
        x-terminal-emulator -e "$ADHD_BIN --headless --config $CONFIG --mcp-addr :0 --log $HEADLESS_LOG --prime-plus --prime-addr http://localhost:$DASHBOARD_PORT/mcp --debug" &
        MANAGED_PIDS+=($!)
    else
        warn "Could not open new terminal. Starting headless in background..."
        "$ADHD_BIN" --headless \
            --config "$CONFIG" \
            --mcp-addr :0 \
            --log "$HEADLESS_LOG" \
            --prime-plus \
            --prime-addr "http://localhost:$DASHBOARD_PORT/mcp" \
            --debug &
        MANAGED_PIDS+=($!)
    fi

    # Wait for headless to start
    sleep 2

    # Terminal 3: Log monitor
    info "Opening Terminal 3 (Log Monitor)..."
    if command -v open &> /dev/null; then
        # macOS - opens in native Terminal app
        open -a Terminal <<EOF
cd $(pwd)
echo "Monitoring JSONL logs from headless..."
sleep 1
tail -f $HEADLESS_LOG | jq -c '{method, type, timestamp}' 2>/dev/null || tail -f $HEADLESS_LOG
EOF
    elif command -v x-terminal-emulator &> /dev/null; then
        # Linux
        x-terminal-emulator -e "tail -f $HEADLESS_LOG | jq -c '{method, type, timestamp}'" &
        MANAGED_PIDS+=($!)
    else
        info "Monitor logs with:"
        info "  tail -f $HEADLESS_LOG | jq '.'"
    fi

    info ""
    success "Demo started in separate terminals!"
    info "This script will now wait. Press Ctrl+C to stop."
    info ""

    # If we have managed PIDs (background processes), wait for them
    if [ ${#MANAGED_PIDS[@]} -gt 0 ]; then
        wait
    else
        # On macOS with native terminals, just wait for user interrupt
        while true; do sleep 1; done
    fi
}

# Query dashboard health
query_health() {
    info "Querying dashboard health..."

    # Check if dashboard is running
    if ! curl -s "http://localhost:$DASHBOARD_PORT/mcp" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"adhd.status","params":{}}' \
        > /dev/null 2>&1; then
        error "Dashboard not responding on localhost:$DASHBOARD_PORT"
        error "Start dashboard first: $0"
        return 1
    fi

    # Query status
    info "Dashboard Status:"
    curl -s "http://localhost:$DASHBOARD_PORT/mcp" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"adhd.status","params":{}}' | jq -r '.result | "Lights: \(.summary.total) total, \(.summary.green) green, \(.summary.red) red, \(.summary.yellow) yellow"'

    echo ""
    info "Lights:"
    curl -s "http://localhost:$DASHBOARD_PORT/mcp" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"adhd.lights.list","params":{}}' | jq -r '.result.lights[] | "  \(.source)/\(.name) → \(.status)"'
}

# Main entry point
main() {
    trap cleanup EXIT

    check_prerequisites
    echo ""

    # If argument provided, use it as choice; otherwise show menu
    if [ $# -gt 0 ]; then
        handle_choice "$1" "$2"
    else
        main_menu
    fi
}

# Run main with all arguments passed through
main "$@"

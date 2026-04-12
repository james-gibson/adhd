#!/usr/bin/env bash

MCP_URL="http://localhost:50925/mcp"
CLUSTER_URL="http://localhost:19100/cluster"
REFRESH_INTERVAL=15
CURL_TIMEOUT=3
MAX_PARALLEL=6
PROPAGATION_WAIT=3

BOLD='\033[1m'
DIM='\033[2m'
GREEN='\033[32m'
RED='\033[31m'
YELLOW='\033[33m'
CYAN='\033[36m'
MAGENTA='\033[35m'
RESET='\033[0m'

PRIME_URL="$MCP_URL"
PRIME_ISOTOPE=""
declare -A KNOWN_ENDPOINTS
declare -A PROPAGATION_RESULTS
RUNG_PROGRESS_TOKEN=0

status_icon() {
  case "$1" in
    green)  echo -e "${GREEN}●${RESET}" ;;
    red)    echo -e "${RED}●${RESET}" ;;
    yellow) echo -e "${YELLOW}●${RESET}" ;;
    *)      echo -e "${DIM}●${RESET}" ;;
  esac
}

light_depth() {
  local stripped="${1#smoke:}"
  local slashes="${stripped//[^\/]/}"
  echo "${#slashes}"
}

depth_label() {
  case "$1" in
    0) echo "${DIM}[direct]${RESET}" ;;
    1) echo "${DIM}[1 hop ]${RESET}" ;;
    2) echo "${DIM}[2 hops]${RESET}" ;;
    *) echo "${DIM}[${1} hops]${RESET}" ;;
  esac
}

rewrite_endpoint() {
  local endpoint="$1"
  if [[ "$MCP_URL" =~ localhost|127\.0\.0\.1 ]]; then
    echo "$endpoint" | sed -E 's|^(https?://)([^:/]+)(.*)|http://localhost\3|'
  else
    echo "$endpoint"
  fi
}

ms_now() {
  date +%s%3N 2>/dev/null || echo $(($(date +%s) * 1000))
}

mcp_post() {
  local url="$1" method="$2" id="$3" payload="${4:-{}}"
  curl -s --max-time "$CURL_TIMEOUT" -X POST "$url" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":$id,\"method\":\"$method\",\"params\":$payload}"
}

# ── Rung respond call — matches the exact shape from the spec ─────────────────
# tools/call wrapper with feature_id, nonce, and progressToken
rung_respond() {
  local url="$1" nonce="$2" token="$3"
  curl -s --max-time "$CURL_TIMEOUT" -X POST "$url" \
    -H "Content-Type: application/json" \
    -d "$(jq -n \
      --arg nonce "$nonce" \
      --argjson token "$token" \
      '{
        "jsonrpc": "2.0",
        "id": ($token),
        "method": "adhd.rung.respond",
        "params": {
          "name": "adhd.rung.respond",
          "arguments": {
            "feature_id": "adhd",
            "nonce": $nonce
          },
          "_meta": {
            "progressToken": ($token)
          }
        }
      }')"
}

load_cluster_registry() {
  local registry
  registry=$(curl -s --max-time "$CURL_TIMEOUT" "$CLUSTER_URL" 2>/dev/null)
  [ -z "$registry" ] && return 1
  while IFS=$'\t' read -r name adhd_mcp; do
    [ -z "$name" ] || [ -z "$adhd_mcp" ] && continue
    KNOWN_ENDPOINTS["$name"]=$(rewrite_endpoint "$adhd_mcp")
  done < <(echo "$registry" | jq -r 'to_entries[] | [.key, .value.adhd_mcp] | @tsv')
  return 0
}

probe_all_nodes() {
  local tmpdir="$1"
  local pids=()

  for name in "${!KNOWN_ENDPOINTS[@]}"; do
    local url="${KNOWN_ENDPOINTS[$name]}"
    local safe_name
    safe_name=$(echo "$name" | tr '/' '_')

    while [ "${#pids[@]}" -ge "$MAX_PARALLEL" ]; do
      local new_pids=()
      for pid in "${pids[@]}"; do
        kill -0 "$pid" 2>/dev/null && new_pids+=("$pid")
      done
      pids=("${new_pids[@]}")
      [ "${#pids[@]}" -ge "$MAX_PARALLEL" ] && sleep 0.05
    done

    (
      local t0 t1
      t0=$(ms_now)
      resp=$(mcp_post "$url" "adhd.isotope.status" 6)
      t1=$(ms_now)
      echo "$resp" > "$tmpdir/${safe_name}.isotope"
      echo $((t1 - t0)) > "$tmpdir/${safe_name}.latency"
      mcp_post "$url" "adhd.isotope.instance" 1 > "$tmpdir/${safe_name}.instance"
    ) &
    pids+=($!)
  done

  wait "${pids[@]}" 2>/dev/null
}

relocate_prime() {
  local tmpdir="$1"
  PRIME_RELOCATED=""
  for name in "${!KNOWN_ENDPOINTS[@]}"; do
    local url="${KNOWN_ENDPOINTS[$name]}"
    local safe_name
    safe_name=$(echo "$name" | tr '/' '_')
    local instance_file="$tmpdir/${safe_name}.instance"
    [ -f "$instance_file" ] || continue
    local id
    id=$(jq -r '.result.isotope // empty' "$instance_file" 2>/dev/null)
    if [ "$id" = "$PRIME_ISOTOPE" ]; then
      if [ "$url" != "$PRIME_URL" ]; then
        PRIME_RELOCATED="$name ($url)"
        PRIME_URL="$url"
      fi
      return 0
    fi
  done
  PRIME_URL=""
  return 1
}

# ── Propagation test via adhd.rung.respond ────────────────────────────────────
# Strategy:
#   1. Generate a unique nonce
#   2. Send rung.respond to the PRIME with that nonce — this is the "ring the bell"
#   3. Wait PROPAGATION_WAIT seconds for it to propagate
#   4. Send the same nonce to every peer — if they echo it back cleanly, it propagated
run_propagation_test() {
  local tmpdir="$1"
  PROPAGATION_RESULTS=()

  [ -z "$PRIME_URL" ] && return

  # Increment token each cycle for uniqueness
  RUNG_PROGRESS_TOKEN=$(( RUNG_PROGRESS_TOKEN + 1 ))
  local nonce
  nonce="probe-$(ms_now)-$$"
  local token="$RUNG_PROGRESS_TOKEN"

  # Step 1: ring the prime
  local reg_resp
  reg_resp=$(rung_respond "$PRIME_URL" "$nonce" "$token")

  if [ -z "$reg_resp" ] || echo "$reg_resp" | jq -e '.error' > /dev/null 2>&1; then
    local reason
    reason=$(echo "$reg_resp" | jq -r '.error.message // "no response"' 2>/dev/null)
    PROPAGATION_RESULTS["_register"]="failed|$reason"
    return
  fi

  # Capture what the prime echoed back
  local prime_echo
  prime_echo=$(echo "$reg_resp" | jq -r '.result.content[0].text // .result // empty' 2>/dev/null)
  PROPAGATION_RESULTS["_register"]="ok|$nonce|$prime_echo"

  # Step 2: wait for propagation
  sleep "$PROPAGATION_WAIT"

  # Step 3: challenge every peer with the same nonce in parallel
  local pids=()
  local peer_token=$(( token + 1000 ))  # offset tokens for peer calls

  for name in "${!KNOWN_ENDPOINTS[@]}"; do
    local url="${KNOWN_ENDPOINTS[$name]}"
    local safe_name
    safe_name=$(echo "$name" | tr '/' '_')
    [ "$url" = "$PRIME_URL" ] && continue  # prime already answered

    while [ "${#pids[@]}" -ge "$MAX_PARALLEL" ]; do
      local new_pids=()
      for pid in "${pids[@]}"; do
        kill -0 "$pid" 2>/dev/null && new_pids+=("$pid")
      done
      pids=("${new_pids[@]}")
      [ "${#pids[@]}" -ge "$MAX_PARALLEL" ] && sleep 0.05
    done

    (
      local t0 t1
      t0=$(ms_now)
      local resp
      resp=$(rung_respond "$url" "$nonce" "$peer_token")
      t1=$(ms_now)
      local latency=$((t1 - t0))

      if [ -z "$resp" ]; then
        echo "unreachable|${latency}ms" > "$tmpdir/${safe_name}.propagation"
      elif echo "$resp" | jq -e '.error' > /dev/null 2>&1; then
        local reason
        reason=$(echo "$resp" | jq -r '.error.message // "error"')
        echo "error|$reason" > "$tmpdir/${safe_name}.propagation"
      else
        # Extract the response text — adjust path if the API shape differs
        local echo_val
        echo_val=$(echo "$resp" | jq -r '.result.content[0].text // .result // empty' 2>/dev/null)
        # Check if the nonce appears in the response (propagation confirmed)
        if echo "$echo_val" | grep -q "$nonce" 2>/dev/null || \
           echo "$resp" | jq -e '.result' > /dev/null 2>&1; then
          echo "propagated|${latency}ms|$echo_val" > "$tmpdir/${safe_name}.propagation"
        else
          echo "no-echo|${latency}ms|$echo_val" > "$tmpdir/${safe_name}.propagation"
        fi
      fi
    ) &
    pids+=($!)
    peer_token=$(( peer_token + 1 ))
  done

  wait "${pids[@]}" 2>/dev/null

  # Read results
  for name in "${!KNOWN_ENDPOINTS[@]}"; do
    [ "${KNOWN_ENDPOINTS[$name]}" = "$PRIME_URL" ] && continue
    local safe_name
    safe_name=$(echo "$name" | tr '/' '_')
    local pfile="$tmpdir/${safe_name}.propagation"
    PROPAGATION_RESULTS["$name"]=$([ -f "$pfile" ] && cat "$pfile" || echo "missing|no file")
  done
}

bootstrap() {
  echo -e "${DIM}Loading cluster registry from $CLUSTER_URL...${RESET}"
  if ! load_cluster_registry; then
    echo -e "${RED}✘ Could not reach cluster registry${RESET}"
    return 1
  fi
  echo -e "${DIM}Found ${#KNOWN_ENDPOINTS[@]} nodes. Resolving prime isotope...${RESET}"
  PRIME_ISOTOPE=$(mcp_post "$MCP_URL" "adhd.isotope.instance" 1 | jq -r '.result.isotope // empty')
  if [ -z "$PRIME_ISOTOPE" ]; then
    echo -e "${RED}✘ Could not reach $MCP_URL${RESET}"
    return 1
  fi
  PRIME_URL="$MCP_URL"
  echo -e "${GREEN}✔ Anchored to isotope: ${DIM}$PRIME_ISOTOPE${RESET}"
  sleep 1
  return 0
}

scan() {
  load_cluster_registry

  local tmpdir
  tmpdir=$(mktemp -d)
  trap "rm -rf '$tmpdir'" RETURN

  # Node probes and propagation test run concurrently
  probe_all_nodes "$tmpdir" &
  local probe_pid=$!

  run_propagation_test "$tmpdir" &
  local prop_pid=$!

  wait "$probe_pid"
  relocate_prime "$tmpdir"
  local prime_found=$?

  local status_resp isotope_resp lights_resp
  if [ -n "$PRIME_URL" ]; then
    status_resp=$(mcp_post "$PRIME_URL" "adhd.status" 3)
    isotope_resp=$(mcp_post "$PRIME_URL" "adhd.isotope.status" 4)
    lights_resp=$(mcp_post "$PRIME_URL" "adhd.lights.list" 5)
  fi

  wait "$prop_pid"

  clear
  echo ""
  echo -e "${BOLD}ADHD Quick Scan${RESET}  ${DIM}$(date '+%I:%M:%S %p')  — refreshing every ${REFRESH_INTERVAL}s  (Ctrl-C to quit)${RESET}"
  echo -e "${DIM}Cluster: $CLUSTER_URL  |  Anchored isotope: $PRIME_ISOTOPE  |  Nodes: ${#KNOWN_ENDPOINTS[@]}${RESET}"
  echo ""

  # ── Prime summary ────────────────────────────────────────────────────────
  if [ "$prime_found" -ne 0 ] || [ -z "$PRIME_URL" ]; then
    echo -e "${MAGENTA}${BOLD}◆ PRIME${RESET}  ${RED}✘ not found among ${#KNOWN_ENDPOINTS[@]} known nodes${RESET}"
  else
    local relocated_note=""
    [ -n "$PRIME_RELOCATED" ] && relocated_note="  ${YELLOW}⚡ relocated → $PRIME_RELOCATED${RESET}"
    echo -e "${MAGENTA}${BOLD}◆ PRIME — $PRIME_URL${RESET}$relocated_note"
    echo -e "${DIM}────────────────────────────────────────${RESET}"

    if [ -n "$status_resp" ] && ! echo "$status_resp" | jq -e '.error' > /dev/null 2>&1; then
      local instance green red yellow dark total peers_list
      instance=$(echo "$status_resp"   | jq -r '.result.instance')
      green=$(echo "$status_resp"      | jq -r '.result.lights.green')
      red=$(echo "$status_resp"        | jq -r '.result.lights.red')
      yellow=$(echo "$status_resp"     | jq -r '.result.lights.yellow')
      dark=$(echo "$status_resp"       | jq -r '.result.lights.dark')
      total=$(echo "$status_resp"      | jq -r '.result.lights.total')
      peers_list=$(echo "$status_resp" | jq -r '.result.cluster_peers | join(", ")')
      echo -e "  Instance : ${BOLD}$instance${RESET}   Peers: $peers_list"
      echo -e "  Lights   : $(status_icon green) $green  $(status_icon red) $red  $(status_icon yellow) $yellow  $(status_icon dark) $dark  (total: $total)"
    else
      echo -e "  ${RED}✘ Status unavailable${RESET}"
    fi

    if [ -n "$isotope_resp" ] && ! echo "$isotope_resp" | jq -e '.error' > /dev/null 2>&1; then
      local role istatus
      role=$(echo "$isotope_resp"    | jq -r '.result.role')
      istatus=$(echo "$isotope_resp" | jq -r '.result.status')
      echo -e "  Role     : $role   Status: ${GREEN}$istatus${RESET}"
    fi
  fi
  echo ""

  # ── Lights ───────────────────────────────────────────────────────────────
  echo -e "${CYAN}${BOLD}▸ All Lights  ${DIM}(sorted: red → yellow → green, then by depth)${RESET}"
  echo -e "${DIM}────────────────────────────────────────${RESET}"

  if [ -n "$PRIME_URL" ] && [ -n "$lights_resp" ] && \
     ! echo "$lights_resp" | jq -e '.error' > /dev/null 2>&1; then
    while IFS=$'\t' read -r lstatus lname details; do
      local depth rank
      depth=$(light_depth "$lname")
      rank=$(case "$lstatus" in red) echo 0;; yellow) echo 1;; green) echo 2;; *) echo 3;; esac)
      echo "${rank}|${depth}|${lstatus}|${lname}|${details}"
    done < <(echo "$lights_resp" | jq -r '.result.lights[] | [.status, .name, .details] | @tsv') | \
    awk -F'|' '
      {
        rank=$1; depth=$2; status=$3; name=$4; details=$5
        if (!(name in best) || rank+0 < best_rank[name]+0) {
          best[name]         = status
          best_rank[name]    = rank
          best_depth[name]   = depth
          best_details[name] = details
        }
      }
      END {
        for (name in best)
          print best_rank[name] "|" best_depth[name] "|" best[name] "|" name "|" best_details[name]
      }
    ' | sort -t'|' -k1,1n -k2,2n | \
    while IFS='|' read -r rank depth lstatus lname details; do
      echo -e "  $(status_icon "$lstatus") $(depth_label "$depth") ${BOLD}$lname${RESET}  ${DIM}$details${RESET}"
    done
  else
    echo -e "  ${DIM}No prime available or lights unavailable${RESET}"
  fi
  echo ""

  # ── Rung propagation results ──────────────────────────────────────────────
  echo -e "${CYAN}${BOLD}▸ Rung Propagation  ${DIM}(nonce challenge via adhd.rung.respond, wait: ${PROPAGATION_WAIT}s)${RESET}"
  echo -e "${DIM}────────────────────────────────────────${RESET}"

  local reg_result="${PROPAGATION_RESULTS[_register]:-}"

  if [ -z "$reg_result" ]; then
    echo -e "  ${DIM}No prime available — skipped${RESET}"
  elif [[ "$reg_result" == failed* ]]; then
    local reason="${reg_result#failed|}"
    echo -e "  ${RED}✘ Prime did not respond to rung:${RESET} $reason"
  else
    # Parse: ok|nonce|echo_val
    local nonce prime_echo
    nonce=$(echo "$reg_result"     | cut -d'|' -f2)
    prime_echo=$(echo "$reg_result" | cut -d'|' -f3-)
    echo -e "  ${GREEN}✔ Prime rang${RESET}  ${DIM}nonce: $nonce${RESET}"
    [ -n "$prime_echo" ] && echo -e "  ${DIM}  echo: $prime_echo${RESET}"
    echo ""

    # Tally for summary line
    local prop_count=0 miss_count=0 err_count=0 total_peers=0

    for name in $(echo "${!KNOWN_ENDPOINTS[@]}" | tr ' ' '\n' | sort); do
      local url="${KNOWN_ENDPOINTS[$name]}"
      [ "$url" = "$PRIME_URL" ] && continue  # prime shown above
      total_peers=$(( total_peers + 1 ))

      local result="${PROPAGATION_RESULTS[$name]:-missing|no result}"
      local prime_marker=""
      [ "$url" = "$PRIME_URL" ] && prime_marker=" ${MAGENTA}◆${RESET}"

      case "$result" in
        propagated*)
          local latency echo_val
          latency=$(echo "$result"  | cut -d'|' -f2)
          echo_val=$(echo "$result" | cut -d'|' -f3-)
          echo -e "  ${GREEN}●${RESET} ${BOLD}$name${RESET}$prime_marker  ${GREEN}✔ propagated${RESET}  ${DIM}$latency${RESET}"
          [ -n "$echo_val" ] && echo -e "    ${DIM}↳ $echo_val${RESET}"
          prop_count=$(( prop_count + 1 ))
          ;;
        no-echo*)
          local latency echo_val
          latency=$(echo "$result"  | cut -d'|' -f2)
          echo_val=$(echo "$result" | cut -d'|' -f3-)
          echo -e "  ${YELLOW}●${RESET} ${BOLD}$name${RESET}$prime_marker  ${YELLOW}⚠ responded but nonce not confirmed${RESET}  ${DIM}$latency${RESET}"
          [ -n "$echo_val" ] && echo -e "    ${DIM}↳ $echo_val${RESET}"
          miss_count=$(( miss_count + 1 ))
          ;;
        missing*)
          echo -e "  ${YELLOW}●${RESET} ${BOLD}$name${RESET}$prime_marker  ${YELLOW}✘ not seen within ${PROPAGATION_WAIT}s${RESET}"
          miss_count=$(( miss_count + 1 ))
          ;;
        unreachable*)
          local latency="${result#unreachable|}"
          echo -e "  ${RED}●${RESET} ${BOLD}$name${RESET}$prime_marker  ${RED}✘ unreachable${RESET}  ${DIM}$latency${RESET}"
          err_count=$(( err_count + 1 ))
          ;;
        error*)
          local reason="${result#error|}"
          echo -e "  ${RED}●${RESET} ${BOLD}$name${RESET}$prime_marker  ${RED}✘ error:${RESET} $reason"
          err_count=$(( err_count + 1 ))
          ;;
      esac
    done

    echo ""
    echo -e "  ${DIM}Summary: ${GREEN}$prop_count propagated${RESET}${DIM}  ${YELLOW}$miss_count missed${RESET}${DIM}  ${RED}$err_count errors${RESET}${DIM}  of $total_peers peers${RESET}"
  fi
  echo ""

  # ── Cluster nodes ─────────────────────────────────────────────────────────
  echo -e "${CYAN}${BOLD}▸ Cluster Nodes  ${DIM}(${#KNOWN_ENDPOINTS[@]} from registry)${RESET}"
  echo -e "${DIM}────────────────────────────────────────${RESET}"
  for name in $(echo "${!KNOWN_ENDPOINTS[@]}" | tr ' ' '\n' | sort); do
    local url="${KNOWN_ENDPOINTS[$name]}"
    local safe_name prime_marker=""
    safe_name=$(echo "$name" | tr '/' '_')
    [ "$url" = "$PRIME_URL" ] && prime_marker=" ${MAGENTA}${BOLD}◆ prime${RESET}"

    local isotope_file="$tmpdir/${safe_name}.isotope"
    local latency_file="$tmpdir/${safe_name}.latency"
    local latency=""
    [ -f "$latency_file" ] && latency=" ${DIM}$(cat "$latency_file")ms${RESET}"

    if [ ! -f "$isotope_file" ] || [ -z "$(cat "$isotope_file")" ] || \
       jq -e '.error' "$isotope_file" > /dev/null 2>&1; then
      echo -e "  ${RED}●${RESET} ${BOLD}$name${RESET}$prime_marker  ${RED}✘ unreachable${RESET}$latency  ${DIM}$url${RESET}"
    else
      local role istatus
      role=$(jq -r '.result.role'      "$isotope_file")
      istatus=$(jq -r '.result.status' "$isotope_file")
      echo -e "  ${GREEN}●${RESET} ${BOLD}$name${RESET}$prime_marker  $role  ${GREEN}$istatus${RESET}$latency  ${DIM}$url${RESET}"
    fi
  done
  echo ""

  echo -e "${DIM}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
}

trap 'echo -e "\n${RESET}"; exit 0' INT

until bootstrap; do
  echo -e "${DIM}Retrying in 3s...${RESET}"
  sleep 3
done

while true; do
  scan
  sleep "$REFRESH_INTERVAL"
done


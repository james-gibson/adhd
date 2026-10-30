# Security

adhd's MCP endpoint is a **control plane**, not a status page. This file states
what is true **today**, names the simple exploits that follow, and marks what is
intended-but-not-built. The point is to keep the design from hiding an exploit
behind an assumption — if a protection is a presumption, it is written here as a
presumption.

The surface is the fire marshal's: smoke-alarm's, reached by proxy through adhd
with the isotopes' help. adhd has an inbound entitlement gate, and it is **off
unless `mcp_server.gate.enabled` is set** (or `--gate` / `--gate-key` is passed).
With it off, the pre-gate contract stands: reachable ⇒ driveable.

## What is true today (verified against the tree)

| Property | State |
| --- | --- |
| Inbound auth / trust check on RPC dispatch | capability gate when `mcp_server.gate.enabled`; **none** otherwise (#27) |
| `initialize` / `tools/list` | gated: an unentitled caller gets an empty facade; a capability sees only its tier |
| Honeypots | only for tools pushed in by a `adhd.cluster.join`; real tools are not decoys |
| Security events filed | `honeypot_triggered`, `rung_challenge_failed`, `unentitled_tool_call`, `stdio_knock` |
| `adhd.proxy` | accepts a **caller-supplied** `target_endpoint` + `auth` — but only for an exec-tier caller, and **URL-only**: a `stdio:` target or a `command`/`args` field is refused and filed as a knock (#41) |
| Stdio transport | named targets from local config only; no request field can name or construct a command (#41) |
| Host / `Origin` validation | **none** (#29) |
| Caller attribution | `r.RemoteAddr` at the tool surface only; the ingress session entry id is not carried |
| Isotope registration | follows config, flag overrides (#26) |
| Advertise guard | refuses an unguarded non-loopback bind unless `--force` (#26) |
| Protocol version | not a gate; `initialize` ignores the client's revision (#28) |

Consequence with the gate **off**: **reachable ⇒ driveable.** With it **on**:
reachable ⇒ an empty facade, and driving it files `unentitled_tool_call`.

The gate is a **capability with a rung**, not an isotope-certified identity, and
the tiers are kb's shield-gate contract (`R/C 0, W 3, E 4, X 6`). A key is a
local file (0600) whose keys carry a maximum rung; a signed `_shield` member or
a bearer `<kid>.<secret>` presents it. Unknown methods are tier 6, never read.

## Simple exploits to know about

These are cheap, they are not exotic, and they are the reason the rows above
matter.

1. **Loopback is not a security boundary.** The advertise guard allows a
   loopback bind because it is inert to the LAN — but any local process, any
   co-tenant on a shared host, and any local user can drive the endpoint.
2. **DNS rebinding.** There is no `Host`/`Origin` check. A web page can rebind
   its own hostname to `127.0.0.1` and POST to `/mcp` as a *same-origin* request,
   with no CORS preflight to stop it. The MCP specification warns about exactly
   this; a localhost bind alone does not close it.
3. **`adhd.proxy` is an open relay.** A caller chooses the target *and* supplies
   the credential, so the instance can be used for SSRF against internal
   endpoints and to relay a token it was handed. This is a feature and an
   exploit at the same time.
4. **Side effects as the adhd user.** `adhd.gh.run` and `adhd.hurl.run` execute
   with adhd's privileges and ambient credentials. "Read-only-looking" is not
   read-only.
5. **Honeypot evasion.** With the gate off, an attacker who ignores the decoys
   and calls the real tools is never logged and files nothing — the panel has a
   trap for the naive and no alarm for the deliberate. With the gate on, a
   refusal files `unentitled_tool_call`, so the same attempt is the alarm.
6. **Attribution defeat.** Under (2) the logged caller address is `127.0.0.1`, so
   the knock's caller-IP record is worthless for a rebound caller. The intended
   fix is *not* a better IP source: **golden attribution is the entry id of the
   session established at the cluster's ingress, and it is not supplied by the
   caller.** The ingress is the admission layer that holds the session (`A` in
   the lab's verification chain, *presence*); the MCP tool surface is the next
   layer (`B`) and holds no session — which is why `RemoteAddr` at `B` is
   structurally the wrong source. `B` should *cite* the entry id `A` assigned,
   carried on the OTel trace and sealed by the isotope so it cannot be forged in
   transit. State today: none of it exists — `internal/telemetry` is a stub, and
   the chain does not yet carry anything from A to B (the alert path is unwired
   upstream, `smoke-alarm#24`).
7. **Replayed or static knocks.** A knock sequence that is not bound to a
   signed, nonce-carrying record is replayable; see the knock section.

## The knock (intended mechanism, partly unbuilt)

A simple knock is an ordered call sequence — the same tools invoked descending
vs ascending, or addressed proxy hops called in scrambled order (Enigma-style).
Three properties must be kept straight:

- **One bit per phase is the unit, not the strength.** The *collection* of phases
  is what the verifier checks against the outcome.
- **The clock order is required.** An external **sidecar** verifies every hop
  against the shared tick, which is what the clock is for. Caution: no sidecar
  exists in the tree yet, and it is the trust anchor for the whole scheme — it
  occupies the position fire-marshal already holds by owning the signing key.
- **A knock is detection, not authorization.** If a knock admits a caller, the
  scheme is obscurity (classic port knocking, replayable unless it carries an
  SPA/HMAC layer). Authorization is the isotope trust chain's decision. Enigma's
  rotors composed into a large space but the settings were *static and secret per
  day* — that is how it fell. Here the phases are *recorded*, so composition buys
  attribution, not secrecy, and it stays an asset only while the phases measure
  rather than authorize.

## What is implemented, and what is still intended

Implemented:

- **The empty facade** (#27): an unentitled caller sees no tools, no peers, no
  lights; `initialize` still connects so a client is told there is nothing.
- **The capability gate** (#27): tiers per method and per tool, filtered at both
  `tools/list` and `tools/call`, with `unentitled_tool_call` filed on refusal.
  A refusal is shaped as "method not found", not 401/403, so a probe learns
  neither that the gate exists nor that the tool it guessed at does.
- **Stdio by name** (#41): a configured target is launched on demand and
  addressed as `adhd.stdio.<name>.call` (or `stdio:<name>` from a project
  endpoint). A request that carries a command is a knock, refused and filed.
  Teardown is by process group, so a wrapper's real server dies with it.

Still intended:

- **Host/Origin validation** (#29) — DNS rebinding still drives a loopback bind
  from a browser, and the gate does not close it: a rebound request arrives with
  no capability and therefore sees only the facade, but the attribution is still
  `127.0.0.1`.
- **The trust chain as authorizer** — the capability is a bearer secret, not a
  negotiated isotope receipt. The shield proves possession; it does not prove
  which implementation answered.
- **The knock as the detector** with the sidecar as external verifier: the
  knock phases are recorded but nothing verifies their order yet.

The gate is opt-in because enabling it by default would lock out every current
consumer at once (Zed's context server, lezz demo, a KB scope). The honest
reading is: on a loopback bind with no gate, treat the surface as driveable.

## FAQ

**Is it safe to expose adhd on the LAN?** Not without the gate. With
`mcp_server.gate.enabled` and a keyring, an unentitled LAN caller sees an empty
facade and every real call files `unentitled_tool_call` — but Host/Origin
validation is still missing (#29), and a bearer capability travels in cleartext
over plain HTTP. Bind loopback, put an auth gateway in front, or keep the LAN
exposure deliberate (`--force`) and gate it.

**How do I give a caller a capability?** `adhd.yaml`:

```yaml
mcp_server:
  enabled: true
  gate:
    enabled: true
    key_file: ~/.config/adhd/gate.key   # created 0600 on first start
```

The keyring holds `read` (0), `write` (3) and `exec` (6) keys. A caller presents
either `Authorization: Bearer <kid>.<secret>` or a signed `_shield` object in
`params` (kid, ts, nonce, rung, epoch, sig), the same contract as kb's shield
gate. Do not retry a rejected signed request with the same nonce: a successful
verification spends it.

**Why can a caller with an exec key use `adhd.proxy` with its own credential?**
Because an exec capability is authorization to use this instance as a proxy hop.
The exploit was that an *anonymous* caller could do it (#27); the remedy is the
tier, not removing the proxy. If that is too much for a credential you issue,
issue `write` or `read` instead.

**Can `adhd.proxy` reach a stdio server?** Not by naming one. Stdio servers are
configured as `stdio_targets` and addressed by name; a request carrying
`stdio:…`, `command`, `cmd`, `args` or `argv` is refused and filed as
`stdio_knock`. The transport exists (#41); the command lives in the operator's
file, never in a request.

**What does a stdio target inherit?** Nothing from adhd's environment unless it
is named in `inherit_env`. A child that inherits `os.Environ()` inherits every
API key adhd was opened with, so `PATH` and `HOME` are passed explicitly. No
shell interprets `command`/`args`.

**Why did adhd refuse to start after I added `smoke_alarm`?** The advertise guard
refuses to register/advertise a non-loopback MCP bind (an unguarded endpoint is an
alarm offense). Bind `127.0.0.1:PORT`, front it with a gateway, or pass `--force`
for a deliberate LAN exposure. See #26.

**Can I use an older MCP client?** The revision is pinned to `2024-11-05` and the
client's requested version is ignored — there is no accept/upgrade path. The
intended rule is to accept an older revision with an upgrade request *when the
trust chain is present* and refuse otherwise; version is not the gate. See #28.

**What happens if someone calls a honeypot tool?** The caller IP is logged, a red
`security/<event>` light is raised, and `honeypot_triggered` is filed to
smoke-alarm's `/federation/report`. The fake result is returned so the trap is not
revealed.

**How is an alarm filed?** `fireSecurity` → `POST <smoke-alarm>/federation/report`
(target-status intake). That leg may itself need to ride `adhd.proxy` for a peer
to reach the alarm; its own authentication was not verified here.

**If a knock phase is one bit, isn't that weak?** Alone, yes — deliberately, it is
the unit. Strength comes from the composition plus the signed record, and the
whole thing is detection. It becomes weak the moment the phase collection is
promoted to authorizer.

**Is a loopback bind enough?** Against the LAN, yes. Against local processes and
DNS rebinding, no.

**If the caller IP is `127.0.0.1`, how is a caller attributed?** Today it is not —
`r.RemoteAddr` is all we log, and rebinding or a local caller makes it useless. By
design, attribution is the **entry id of the ingress session**: assigned when the
caller enters the cluster, never supplied by the caller. The tool surface *cites*
that id rather than deriving an identity from an address. Neither the ingress
session id nor the trace that would carry it exists in adhd yet.

## Reporting

Use GitHub's private vulnerability reporting on this repository for anything
sensitive. For design-level findings of the kind above, a public issue is fine —
they are exactly what this file exists to surface.

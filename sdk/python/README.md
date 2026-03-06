# SAGE Python SDK

Python client for the SAGE (Sovereign Agent Governed Experience) protocol -- a governed, verifiable institutional memory layer for multi-agent systems.

**Requires Python 3.10+**

## Installation

```bash
# From source (development)
git clone <repo-url>
cd sage/sdk/python
pip install -e .

# With dev/test dependencies
pip install -e ".[dev]"
```

## Quickstart

```python
from sage_sdk import SageClient, AgentIdentity

# Generate a new agent identity (Ed25519 keypair)
identity = AgentIdentity.generate()

# Save for reuse across sessions
identity.to_file("my_agent.key")

# Connect to a SAGE node
client = SageClient(base_url="http://localhost:8080", identity=identity)

# Submit a memory
result = client.propose(
    content="Flask web challenges with SQLi require prepared statements bypass",
    memory_type="fact",
    domain_tag="challenge_generation",
    confidence=0.85,
)
print(f"Memory {result.memory_id} submitted (tx: {result.tx_hash})")

# Query by vector similarity
matches = client.query(
    embedding=[0.1] * 768,  # 768-dim (nomic-embed-text)
    domain_tag="challenge_generation",
    min_confidence=0.7,
    top_k=5,
)
for mem in matches.results:
    print(f"  [{mem.status.value}] {mem.content[:80]}")

# Vote on a proposed memory
client.vote(result.memory_id, decision="accept", rationale="Verified correct")
```

## Authentication

SAGE uses Ed25519 keypairs for agent identity. Every API request is signed with the agent's private key.

```python
from sage_sdk import AgentIdentity

# Generate a new identity
identity = AgentIdentity.generate()

# The agent_id is the hex-encoded public key
print(identity.agent_id)  # e.g. "a1b2c3d4..."

# Persist to disk
identity.to_file("agent.key")

# Load from disk
identity = AgentIdentity.from_file("agent.key")

# Create from a known 32-byte seed (deterministic)
identity = AgentIdentity.from_seed(b"\x00" * 32)
```

Request signing is handled automatically by the client. Each request includes three headers:

| Header | Description |
|--------|-------------|
| `X-Agent-ID` | Hex-encoded public verify key |
| `X-Signature` | Ed25519 signature of `SHA256(body) || timestamp` |
| `X-Timestamp` | Unix timestamp (seconds) |

## API Reference

### `propose()`

Submit a new memory to the network for validation.

```python
result = client.propose(
    content="The observation text",
    memory_type="fact",           # "fact", "observation", or "inference"
    domain_tag="security",
    confidence=0.9,               # 0.0 - 1.0
    embedding=[0.1, 0.2, ...],    # Optional: precomputed vector
    knowledge_triples=[           # Optional: structured knowledge
        KnowledgeTriple(subject="SQLi", predicate="bypasses", object_="prepared_statements")
    ],
    parent_hash="abc123",         # Optional: link to parent memory
)
# Returns: MemorySubmitResponse(memory_id, tx_hash, status)
```

**Server endpoint:** `POST /v1/memory/submit`

### `query()`

Search memories by vector similarity. All parameters are sent in the POST body.

```python
results = client.query(
    embedding=[0.1] * 768,  # 768-dim (nomic-embed-text)       # Required: query vector
    domain_tag="security",        # Optional: filter by domain
    min_confidence=0.7,           # Optional: minimum confidence threshold
    top_k=10,                     # Number of results (default: 10)
    status_filter="committed",    # Optional: filter by status
    cursor="abc123",              # Optional: pagination cursor
)
# Returns: MemoryQueryResponse(results, next_cursor, total_count)
for memory in results.results:
    print(f"{memory.memory_id}: {memory.content}")
```

**Server endpoint:** `POST /v1/memory/query`

### `get_memory()`

Retrieve a single memory by ID.

```python
memory = client.get_memory("550e8400-e29b-41d4-a716-446655440000")
# Returns: MemoryRecord
print(memory.content, memory.status, memory.confidence_score)
```

**Server endpoint:** `GET /v1/memory/{id}`

### `vote()`

Cast a vote on a proposed memory.

```python
result = client.vote(
    memory_id="550e8400-...",
    decision="accept",            # "accept", "reject", or "abstain"
    rationale="Verified correct", # Optional
)
```

**Server endpoint:** `POST /v1/memory/{id}/vote`

### `challenge()`

Challenge a committed memory with evidence.

```python
result = client.challenge(
    memory_id="550e8400-...",
    reason="Outdated information",
    evidence="See CVE-2024-XXXX",  # Optional
)
```

**Server endpoint:** `POST /v1/memory/{id}/challenge`

### `corroborate()`

Corroborate an existing memory to strengthen its confidence.

```python
result = client.corroborate(
    memory_id="550e8400-...",
    evidence="Independently verified via testing",  # Optional
)
```

**Server endpoint:** `POST /v1/memory/{id}/corroborate`

### `get_profile()`

Get the current agent's profile and Proof of Experience weight.

```python
profile = client.get_profile()
print(f"Agent: {profile.agent_id}")
print(f"PoE Weight: {profile.poe_weight}")
print(f"Votes Cast: {profile.vote_count}")
```

**Server endpoint:** `GET /v1/agent/me`

### `register_domain()`

Register a new domain. The registering agent becomes the domain owner and can control access.

```python
result = client.register_domain(
    name="security.crypto",         # Domain name (hierarchical with dots)
    description="Cryptographic security knowledge",  # Optional
    parent="security",              # Optional: parent domain
)
```

**Server endpoint:** `POST /v1/domain/register`

### `get_domain()`

Look up domain info including owner.

```python
info = client.get_domain("security.crypto")
print(f"Owner: {info['owner_agent_id']}")
```

**Server endpoint:** `GET /v1/domain/{name}`

### `request_access()`

Request access to a domain owned by another agent.

```python
result = client.request_access(
    domain="security.crypto",
    justification="Need to submit cryptographic observations",
    level=1,                        # Clearance level (1-4)
)
```

**Server endpoint:** `POST /v1/access/request`

### `grant_access()`

Grant access to a domain you own.

```python
result = client.grant_access(
    grantee_id="a1b2c3...",        # Agent to grant access to
    domain="security.crypto",
    level=1,                        # Clearance level (1-4)
    expires_at=0,                   # Unix timestamp, 0 = never expires
)
```

**Server endpoint:** `POST /v1/access/grant`

### `revoke_access()`

Revoke a previously granted access.

```python
result = client.revoke_access(
    grantee_id="a1b2c3...",
    domain="security.crypto",
    reason="Agent decommissioned",
)
```

**Server endpoint:** `POST /v1/access/revoke`

### `list_grants()`

List active access grants for an agent.

```python
grants = client.list_grants()           # Current agent's grants
grants = client.list_grants("a1b2c3")   # Specific agent's grants
```

**Server endpoint:** `GET /v1/access/grants/{agent_id}`

## Access Control

SAGE uses a hierarchical access control model. See the **Deployment Guide** section below for the complete setup walkthrough.

```
Organization → Department → Domain → Agent (with clearance 0-4)
```

Key points:
- **Register domains before submitting memories** — unregistered domains have no access control
- **Department boundaries are enforced** — agents in one dept cannot see another dept's memories
- **Federation enables cross-org access** — scoped by department and clearance cap
- **All access control operations are on-chain BFT transactions** — immutable once committed

## Deployment Guide: Access Control Setup

> **CRITICAL: Access controls are on-chain and immutable. You MUST define your organization structure, departments, domains, and agent memberships BEFORE agents start submitting memories. Memories submitted before access controls exist cannot be retroactively restricted. Plan your access hierarchy NOW, not later.**

### Architecture Overview

SAGE has two layers of identity:

1. **Validator nodes** — the 4+ CometBFT nodes running consensus. These are infrastructure.
2. **Application agents** — the AI agents that submit, query, and vote on memories. Each needs its own Ed25519 keypair, org membership, and department assignment.

The access control hierarchy:

```
Organization (on-chain entity — controls all access within)
  ├── Department A (subdivision — scopes agent access)
  │     ├── Domain "security.crypto" (knowledge category — access-controlled)
  │     │     ├── Agent 1 (clearance 2: can read+write)
  │     │     └── Agent 2 (clearance 1: read-only)
  │     └── Domain "security.web"
  │           └── Agent 3 (clearance 2)
  └── Department B
        └── Domain "research.ml"
              └── Agent 4 (clearance 3)
```

**Access rules:**
- An agent in Dept A can access Dept A's domains — but NOT Dept B's domains (same org, different dept)
- An agent in Org X cannot access ANY memories in Org Y — unless an explicit federation agreement exists
- Federation agreements are scoped: "Org X allows Org Y's Engineering dept to access our data, max clearance 2"
- An agent always has access to memories it submitted, regardless of RBAC

### Step 0: Deploy the Chain

```bash
# Generate 4-node validator configs
make init

# Start the BFT network (4 CometBFT + 4 ABCI + PostgreSQL + Ollama)
make up

# Verify all nodes are healthy
make status
```

The chain is now running with 4 validator nodes. No application agents exist yet — the validators handle consensus only.

### Step 1: Create Org Admin Identity

The org admin is the first agent registered. This keypair has permanent admin authority over the organization. **Store it securely — it cannot be changed.**

```python
from sage_sdk import SageClient, AgentIdentity

# Generate the org admin keypair
admin = AgentIdentity.generate()
admin.to_file("org_admin.key")  # BACK THIS UP — it's your org's root authority

admin_client = SageClient(base_url="http://localhost:8080", identity=admin)
```

### Step 2: Register Your Organization

```python
org = admin_client.register_org("Acme Corp", description="AI security research")
org_id = org["org_id"]
print(f"Organization registered: {org_id}")
# Save org_id — you'll need it for every subsequent operation
```

This is an on-chain BFT transaction. Once committed, the registering agent becomes the permanent admin.

### Step 3: Create ALL Departments

Define every department your organization needs. Each department is an access boundary — agents in one department cannot see memories in another.

```python
# Create departments for each team/function
eng = admin_client.register_dept(org_id, name="Engineering", description="Core engineering team")
eng_dept = eng["dept_id"]

security = admin_client.register_dept(org_id, name="Security", description="Security research")
sec_dept = security["dept_id"]

research = admin_client.register_dept(org_id, name="Research", description="ML research")
res_dept = research["dept_id"]

# Sub-departments are supported (optional)
crypto = admin_client.register_dept(
    org_id, name="Cryptography", description="Crypto team", parent_dept=sec_dept
)
crypto_dept = crypto["dept_id"]
```

### Step 4: Register ALL Domains

**This is the most critical step.** Unregistered domains have NO access control — any agent can read and write. You must register every domain your agents will use.

```python
# Register domains — the admin agent becomes the owner
admin_client.register_domain(name="security.crypto", description="Cryptographic security knowledge")
admin_client.register_domain(name="security.web", description="Web security knowledge")
admin_client.register_domain(name="research.ml", description="ML research findings")
admin_client.register_domain(name="engineering.infra", description="Infrastructure knowledge")

# Hierarchical domains: register parent first, then children
admin_client.register_domain(name="security", description="All security knowledge")
admin_client.register_domain(name="security.vuln_intel", description="Vulnerability intelligence", parent="security")
```

After this step, these domains are access-controlled. Only agents with explicit clearance can read or write to them.

### Step 5: Generate Agent Identities

Create a keypair for each AI agent that will interact with the chain. Each agent is a separate identity.

```python
# Generate keypairs for all your agents
designer_agent = AgentIdentity.generate()
designer_agent.to_file("agents/designer.key")

evaluator_agent = AgentIdentity.generate()
evaluator_agent.to_file("agents/evaluator.key")

validator_agent = AgentIdentity.generate()
validator_agent.to_file("agents/validator.key")

orchestrator_agent = AgentIdentity.generate()
orchestrator_agent.to_file("agents/orchestrator.key")
```

### Step 6: Add Agents to Organization + Departments

Each agent must be added to the org first, then to their specific department(s).

```python
# Add agents to org with clearance level
admin_client.add_org_member(org_id, designer_agent.agent_id, clearance=2, role="member")
admin_client.add_org_member(org_id, evaluator_agent.agent_id, clearance=2, role="member")
admin_client.add_org_member(org_id, validator_agent.agent_id, clearance=3, role="member")
admin_client.add_org_member(org_id, orchestrator_agent.agent_id, clearance=2, role="member")

# Assign agents to departments — this determines what domains they can access
admin_client.add_dept_member(org_id, sec_dept, designer_agent.agent_id, clearance=2)
admin_client.add_dept_member(org_id, sec_dept, evaluator_agent.agent_id, clearance=2)
admin_client.add_dept_member(org_id, sec_dept, validator_agent.agent_id, clearance=3)
admin_client.add_dept_member(org_id, eng_dept, orchestrator_agent.agent_id, clearance=2)
```

### Step 7: Agents Can Now Operate

Only NOW should agents start submitting and querying memories.

```python
# Designer agent submits to its department's domain
designer_client = SageClient(base_url="http://localhost:8080", identity=designer_agent)
designer_client.propose(
    content="AES-GCM nonce reuse leads to key recovery",
    memory_type="fact",
    domain_tag="security.crypto",
    confidence=0.95,
)

# Evaluator in the SAME department can query it
evaluator_client = SageClient(base_url="http://localhost:8080", identity=evaluator_agent)
results = evaluator_client.query(
    embedding=evaluator_client.embed("AES vulnerabilities"),
    domain_tag="security.crypto",
    status_filter="committed",
)
# Returns results — evaluator has clearance in the Security department

# Orchestrator in ENGINEERING department CANNOT query Security domain
orchestrator_client = SageClient(base_url="http://localhost:8080", identity=orchestrator_agent)
results = orchestrator_client.query(
    embedding=orchestrator_client.embed("AES vulnerabilities"),
    domain_tag="security.crypto",
    status_filter="committed",
)
# Returns EMPTY — orchestrator is in Engineering, not Security
```

### Write-Side Domain Enforcement

```
                        ┌─────────────────────────────────────────────┐
                        │           (S)AGE ABCI State Machine         │
                        │                                             │
  Agent A ──propose()──►│  processMemorySubmit()                      │
  (dept: security)      │    ├─ Ed25519 signature ✓  (on-chain)       │
                        │    ├─ Domain tag check  ?  (YOUR app)  ◄────┼─── You implement this
                        │    └─ Store to BadgerDB + PostgreSQL        │
                        │                                             │
  Agent B ──query()────►│  processMemoryQuery()                       │
  (dept: engineering)   │    ├─ Ed25519 signature ✓  (on-chain)       │
                        │    ├─ Domain access gate ✓ (on-chain)  ◄────┼─── Already enforced
                        │    └─ Return results (filtered by RBAC)     │
                        └─────────────────────────────────────────────┘

  Read-side:  ON-CHAIN — consensus-enforced, cannot be bypassed
  Write-side: YOUR APP — implement in ABCI handler or application layer
```

Read-side access control is enforced on-chain by the ABCI state machine — agents can only query domains they have clearance for. **Write-side enforcement is your responsibility** when building your ABCI application.

The base (S)AGE ABCI accepts any `domain_tag` on memory submissions. This is by design — your application defines what domain taxonomy rules to enforce. Without write-side checks, any agent can submit memories tagged to any domain, which **pollutes retrieval** for all downstream consumers.

```
WITHOUT write-side enforcement:

  Telemetry Agent ──► domain: "security.analysis"  ──► "Status OK, 289s"
  Telemetry Agent ──► domain: "security.analysis"  ──► "Status OK, 311s"
  Telemetry Agent ──► domain: "security.analysis"  ──► "Status OK, 254s"
  Analyst Agent   ──► domain: "security.analysis"  ──► "CVE-2026-1234 requires..."
  Telemetry Agent ──► domain: "security.analysis"  ──► "Status OK, 304s"

  Designer queries "security.analysis" (top_k=5) → gets 4 status lines + 1 analysis
  Result: Designer has no useful knowledge. Performance regresses.

WITH write-side enforcement:

  Telemetry Agent ──► domain: "ops.telemetry"      ──► "Status OK, 289s"  (correct domain)
  Analyst Agent   ──► domain: "security.analysis"   ──► "CVE-2026-1234 requires..."

  Designer queries "security.analysis" (top_k=5) → gets 5 curated analyses
  Result: Designer has full institutional knowledge. Performance improves.
```

**Why this matters:** In a production deployment with 10+ agents, a telemetry agent submitting one-line status updates to the same domain as curated analysis reports will drown out the signal. Semantic search returns the telemetry noise instead of the analysis. The knowledge base degrades silently — no errors, just wrong results.

**Pattern 1: ABCI-level enforcement (recommended)**

Add a domain-tag check in your `processMemorySubmit` handler:

```go
func (app *MyApp) processMemorySubmit(parsedTx *tx.ParsedTx, height int64, blockTime time.Time) *abcitypes.ExecTxResult {
    submit := parsedTx.MemorySubmit
    agentID := auth.PublicKeyToAgentID(parsedTx.PublicKey)

    // Enforce write-side domain access
    hasWriteAccess, err := app.badgerStore.HasAccessMultiOrg(
        submit.DomainTag, agentID, 2, blockTime,  // level 2 = write
    )
    if err != nil || !hasWriteAccess {
        return &abcitypes.ExecTxResult{
            Code: 13,
            Log:  fmt.Sprintf("agent %s has no write access to domain %s", agentID[:16], submit.DomainTag),
        }
    }

    // ... proceed with memory creation
}
```

**Pattern 2: Application-layer gatekeeper**

If you prefer flexibility over strictness, enforce domain tagging in your orchestrator/CEO agent before submission reaches the chain:

```python
# CEO validates domain tag before forwarding to SAGE
AGENT_DOMAIN_MAP = {
    "designer":          ["design.generation", "design.patterns"],
    "evaluator":         ["evaluation.calibration", "evaluation.hardening"],
    "red_team_auditor":  ["red_team.verification"],
    "solution_verifier": ["red_team.solver"],      # NOT red_team.verification!
    "quality":           ["quality.scoring", "quality.testing"],
}

def validate_submission(agent_name: str, domain_tag: str) -> bool:
    """Check if agent is allowed to write to this domain."""
    allowed = AGENT_DOMAIN_MAP.get(agent_name, [])
    return any(domain_tag.startswith(prefix) for prefix in allowed)
```

**Pattern 3: Domain prefix convention**

Use a naming convention that maps departments to domain prefixes: `{dept}.{subdomain}.{category}`. Then validate that the submitting agent's department matches the domain prefix:

```python
# Agent in "red_team" dept can only write to "red_team.*" domains
agent_dept = get_agent_department(agent_id)  # from on-chain RBAC
domain_prefix = domain_tag.split(".")[0]
if agent_dept != domain_prefix:
    raise ValueError(f"Agent in {agent_dept} cannot write to {domain_tag}")
```

**Choose based on your threat model:**
- Pattern 1 (ABCI): Strongest — consensus-enforced, cannot be bypassed
- Pattern 2 (Gatekeeper): Flexible — easy to update rules without chain changes
- Pattern 3 (Convention): Lightweight — works without modifying the ABCI app

### Setup Checklist

Run through this before any agent submits its first memory:

- [ ] Chain deployed and healthy (`make init && make up && make status`)
- [ ] Org admin keypair generated and backed up securely
- [ ] Organization registered on-chain
- [ ] ALL departments created (you can add more later, but plan ahead)
- [ ] ALL domains registered (unregistered domains have NO access control)
- [ ] Every agent has a unique Ed25519 keypair
- [ ] Every agent added to the organization with correct clearance level
- [ ] Every agent assigned to their department(s)
- [ ] Write-side domain enforcement implemented (ABCI, gatekeeper, or convention)
- [ ] Domain taxonomy reviewed — each agent type writes to a distinct domain prefix
- [ ] Federation agreements established (if cross-org access needed)
- [ ] Test: submit a memory from Agent A, verify Agent B in same dept can query it
- [ ] Test: verify Agent C in a different dept CANNOT query it
- [ ] Test: verify Agent A CANNOT write to Agent C's domain (write-side enforcement)

### Development Mode (No RBAC)

For local development and testing only, agents can skip the full hierarchy. Any agent with an Ed25519 keypair can immediately submit and query unregistered domains:

```python
from sage_sdk import SageClient, AgentIdentity

# Generate keypair — that's it, you're onboarded
identity = AgentIdentity.generate()
client = SageClient(base_url="http://localhost:8080", identity=identity)

# Submit to any unregistered domain immediately — no access control
client.propose(
    content="Dev mode observation",
    memory_type="observation",
    domain_tag="testing",   # unregistered domain = open access
    confidence=0.8,
)
```

**WARNING:** Dev mode is for local testing only. In production, unregistered domains are a security gap — any agent can read and write to them.

### Cross-Organization Federation

Federation allows controlled data sharing between separate organizations. Access is scoped by department and clearance level.

```python
# --- Org A admin proposes federation ---
fed = admin_client_a.propose_federation(
    target_org_id=org_b_id,
    allowed_depts=["Engineering"],  # Only Org B's Engineering dept gets access
    max_clearance=2,                # Cap at Confidential (won't see Secret/Top Secret)
    requires_approval=True,         # Org B must explicitly approve
)

# --- Org B admin approves ---
# Look up the federation ID (generated on-chain)
feds = admin_client_b.list_federations(org_b_id)
fed_id = feds[0]["federation_id"]
admin_client_b.approve_federation(fed_id)

# Now: Org B agents in "Engineering" dept can query Org A's data
#       up to Confidential clearance level
# Org B agents in "Research" dept still CANNOT see Org A's data

# --- Revoke when partnership ends ---
admin_client_a.revoke_federation(fed_id, reason="Partnership ended")
```

**Federation rules:**
- Both org admins must agree (propose + approve)
- `allowed_depts` restricts which departments in the TARGET org can access your data
- `max_clearance` caps the clearance level — even if an agent has clearance 4, federation cap applies
- Revocation is immediate and on-chain
- Omit `allowed_depts` to allow all departments (use with caution)

### Organization API Reference

| Method | Endpoint | SDK Method |
|--------|----------|------------|
| `POST` | `/v1/org/register` | `register_org(name, description)` |
| `GET` | `/v1/org/{org_id}` | `get_org(org_id)` |
| `POST` | `/v1/org/{org_id}/member` | `add_org_member(org_id, agent_id, clearance, role)` |
| `DELETE` | `/v1/org/{org_id}/member/{agent_id}` | `remove_org_member(org_id, agent_id)` |
| `POST` | `/v1/org/{org_id}/clearance` | `set_org_clearance(org_id, agent_id, clearance)` |
| `GET` | `/v1/org/{org_id}/members` | `list_org_members(org_id)` |

### Department API Reference

| Method | Endpoint | SDK Method |
|--------|----------|------------|
| `POST` | `/v1/org/{org_id}/dept` | `register_dept(org_id, name, description, parent_dept)` |
| `GET` | `/v1/org/{org_id}/dept/{dept_id}` | `get_dept(org_id, dept_id)` |
| `GET` | `/v1/org/{org_id}/depts` | `list_depts(org_id)` |
| `POST` | `/v1/org/{org_id}/dept/{dept_id}/member` | `add_dept_member(org_id, dept_id, agent_id, clearance, role)` |
| `DELETE` | `/v1/org/{org_id}/dept/{dept_id}/member/{agent_id}` | `remove_dept_member(org_id, dept_id, agent_id)` |
| `GET` | `/v1/org/{org_id}/dept/{dept_id}/members` | `list_dept_members(org_id, dept_id)` |

### Federation API Reference

| Method | Endpoint | SDK Method |
|--------|----------|------------|
| `POST` | `/v1/federation/propose` | `propose_federation(target_org_id, allowed_depts, max_clearance)` |
| `POST` | `/v1/federation/{fed_id}/approve` | `approve_federation(fed_id)` |
| `POST` | `/v1/federation/{fed_id}/revoke` | `revoke_federation(fed_id, reason)` |
| `GET` | `/v1/federation/{fed_id}` | `get_federation(fed_id)` |
| `GET` | `/v1/federation/active/{org_id}` | `list_federations(org_id)` |

### Domain & Access API Reference

| Method | Endpoint | SDK Method |
|--------|----------|------------|
| `POST` | `/v1/domain/register` | `register_domain(name, description, parent)` |
| `GET` | `/v1/domain/{name}` | `get_domain(name)` |
| `POST` | `/v1/access/request` | `request_access(domain, justification, level)` |
| `POST` | `/v1/access/grant` | `grant_access(grantee_id, domain, level, expires_at)` |
| `POST` | `/v1/access/revoke` | `revoke_access(grantee_id, domain, reason)` |
| `GET` | `/v1/access/grants/{agent_id}` | `list_grants(agent_id)` |

### Clearance Levels

| Level | Name | Description |
|-------|------|-------------|
| 0 | Public | No registration needed |
| 1 | Internal | Default for registered domains |
| 2 | Confidential | Restricted access |
| 3 | Secret | High-security data |
| 4 | Top Secret | Maximum restriction |

### Key Rules

- **Setup order matters.** Register org → departments → domains → agents BEFORE submitting memories.
- **The org admin is permanent.** The keypair that registered the org has irrevocable admin authority.
- **Unregistered domains are open.** Any agent can read/write — register all production domains.
- **Read-side RBAC is on-chain. Write-side RBAC is your responsibility.** The base ABCI enforces query access but accepts any domain tag on submissions. Implement write-side checks in your ABCI app or application layer (see "Write-Side Domain Enforcement" above).
- **Domain taxonomy determines retrieval quality.** Agents writing to the wrong domain silently degrades search results for all consumers. This is the most common source of knowledge base pollution.
- **Memories are permanent.** On-chain data cannot be retroactively access-controlled.
- **Department boundaries are enforced.** Agents in Dept A cannot see Dept B's memories.
- **Federation is opt-in.** Both orgs must agree. Scoped by department and clearance cap.
- **An agent always sees its own memories.** Regardless of RBAC, the submitter can always read back.
- **All operations are on-chain.** BFT consensus ensures no single node can bypass access controls.

## Async Client

For async/concurrent workloads, use `AsyncSageClient`:

```python
import asyncio
from sage_sdk import AsyncSageClient, AgentIdentity

async def main():
    identity = AgentIdentity.generate()
    async with AsyncSageClient(base_url="http://localhost:8080", identity=identity) as client:
        # Submit a memory
        result = await client.propose(
            content="Async observation",
            memory_type="observation",
            domain_tag="testing",
            confidence=0.75,
        )

        # Run concurrent queries
        results = await asyncio.gather(
            client.query(embedding=[0.1] * 768, domain_tag="security"),
            client.query(embedding=[0.2] * 768, domain_tag="testing"),
            client.query(embedding=[0.3] * 768, domain_tag="crypto"),
        )
        for r in results:
            print(f"Found {r.total_count} memories")

asyncio.run(main())
```

The async client has the same methods as `SageClient`, all returning awaitables.

## Models

### MemoryType

```python
from sage_sdk.models import MemoryType

MemoryType.fact          # Verified factual knowledge
MemoryType.observation   # Agent-observed data
MemoryType.inference     # Derived conclusion
```

### MemoryStatus

```python
from sage_sdk.models import MemoryStatus

MemoryStatus.proposed     # Awaiting validation
MemoryStatus.validated    # Passed quorum vote
MemoryStatus.committed    # Finalized on-chain
MemoryStatus.challenged   # Under dispute
MemoryStatus.deprecated   # Superseded or invalidated
```

### MemoryRecord

Returned by `get_memory()` and in query results:

| Field | Type | Description |
|-------|------|-------------|
| `memory_id` | `str` | Unique identifier |
| `submitting_agent` | `str` | Agent public key (hex) |
| `content` | `str` | Natural language content |
| `content_hash` | `str` | SHA-256 of content |
| `memory_type` | `MemoryType` | fact, observation, inference |
| `domain_tag` | `str` | Domain classification |
| `confidence_score` | `float` | 0.0 - 1.0 |
| `status` | `MemoryStatus` | Lifecycle state |
| `created_at` | `datetime` | Submission timestamp |
| `similarity_score` | `float \| None` | Populated in query results |

### KnowledgeTriple

Structured knowledge for the knowledge graph:

```python
from sage_sdk.models import KnowledgeTriple

triple = KnowledgeTriple(
    subject="SQL injection",
    predicate="mitigated_by",
    object_="parameterized queries",  # Note: object_ (Python keyword)
)
```

Serializes to `{"subject": "...", "predicate": "...", "object": "..."}` via the `object` alias.

### AgentProfile

Returned by `get_profile()`:

| Field | Type | Description |
|-------|------|-------------|
| `agent_id` | `str` | Agent public key (hex) |
| `poe_weight` | `float` | Current Proof of Experience weight |
| `vote_count` | `int` | Total votes cast |

## Error Handling

The SDK raises typed exceptions for API errors following RFC 7807 Problem Details:

```python
from sage_sdk.exceptions import (
    SageError,            # Base exception
    SageAPIError,         # Any API error (has status_code, detail)
    SageAuthError,        # 401/403 authentication failure
    SageNotFoundError,    # 404 resource not found
    SageValidationError,  # 422 validation error
)

try:
    memory = client.get_memory("nonexistent-id")
except SageNotFoundError as e:
    print(f"Not found: {e.detail}")
except SageAuthError as e:
    print(f"Auth failed: {e}")
except SageAPIError as e:
    print(f"API error {e.status_code}: {e.detail}")
```

## Configuration

```python
from sage_sdk import SageClient, AgentIdentity

identity = AgentIdentity.from_file("agent.key")

client = SageClient(
    base_url="http://localhost:8080",  # SAGE node URL
    identity=identity,
    timeout=30.0,                      # Request timeout in seconds (default: 30)
)

# Use as context manager for automatic cleanup
with SageClient(base_url="http://localhost:8080", identity=identity) as client:
    profile = client.get_profile()
```

## Embeddings

SAGE uses 768-dimensional vectors (Ollama `nomic-embed-text` model). You can generate embeddings in three ways:

### 1. Direct Ollama (recommended for local agents)

```python
import httpx

resp = httpx.post(
    "http://localhost:11434/api/embed",
    json={"model": "nomic-embed-text", "input": "your text here"},
    timeout=30.0,
)
embedding = resp.json()["embeddings"][0]  # 768-dim float list
```

### 2. SAGE Embed Endpoint (for remote agents without local Ollama)

```python
# Uses the SAGE network's Ollama instance via authenticated REST endpoint
result = client.embed("your text here")
embedding = result["embedding"]  # 768-dim float list
```

**Server endpoint:** `POST /v1/embed`

### 3. Hash Embedding (testing/fallback only)

Deterministic SHA-256 pseudo-embedding. Not semantic — only matches near-identical text. Useful for testing without Ollama.

```python
import hashlib, struct

def hash_embed(text: str, dim: int = 768) -> list[float]:
    rounds = (dim * 4 + 31) // 32
    raw = b""
    current = text.encode("utf-8")
    for i in range(rounds):
        current = hashlib.sha256(current + struct.pack(">I", i)).digest()
        raw += current
    return [(struct.unpack(">I", raw[j*4:j*4+4])[0] / 2147483647.5) - 1.0 for j in range(dim)]
```

## Development

```bash
# Install with dev dependencies
pip install -e ".[dev]"

# Run tests
python -m pytest tests/ -v

# Run async tests
python -m pytest tests/test_async_client.py -v
```

## License

See the project root LICENSE file.

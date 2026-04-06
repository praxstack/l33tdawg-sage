#!/usr/bin/env bash
set -euo pipefail

# Generate 4-node CometBFT testnet configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GENESIS_DIR="${SCRIPT_DIR}/genesis"
NUM_VALIDATORS=4

echo "==> Generating ${NUM_VALIDATORS}-node testnet configuration..."

# Clean existing configs
rm -rf "${GENESIS_DIR}"
mkdir -p "${GENESIS_DIR}"

# Check if cometbft binary is available
if ! command -v cometbft &> /dev/null; then
    echo "cometbft binary not found. Building from Docker..."
    HOST_UID=$(id -u)
    HOST_GID=$(id -g)
    docker run --rm -v "${GENESIS_DIR}:/genesis" \
        golang:1.22-alpine sh -c '
        apk add --no-cache git make >/dev/null 2>&1
        git clone --branch v0.38.15 --depth 1 https://github.com/cometbft/cometbft.git /tmp/cometbft 2>/dev/null
        cd /tmp/cometbft && CGO_ENABLED=0 go build -o /usr/local/bin/cometbft ./cmd/cometbft 2>/dev/null
        cometbft testnet \
            --v '"${NUM_VALIDATORS}"' \
            --o /genesis \
            --hostname-prefix cometbft \
            --populate-persistent-peers
        chown -R '"${HOST_UID}:${HOST_GID}"' /genesis
    '
else
    cometbft testnet \
        --v ${NUM_VALIDATORS} \
        --o "${GENESIS_DIR}" \
        --hostname-prefix cometbft \
        --populate-persistent-peers
fi

# Patch config.toml for each node
for i in $(seq 0 $((NUM_VALIDATORS - 1))); do
    CONFIG="${GENESIS_DIR}/node${i}/config/config.toml"

    echo "==> Patching node${i} config..."

    # Disable PEX (use persistent peers only)
    sed -i.bak 's/pex = true/pex = false/' "$CONFIG"

    # Allow non-routable addresses (Docker)
    sed -i.bak 's/addr_book_strict = true/addr_book_strict = false/' "$CONFIG"

    # Allow duplicate IPs (Docker)
    sed -i.bak 's/allow_duplicate_ip = false/allow_duplicate_ip = true/' "$CONFIG"

    # Set block time
    sed -i.bak 's/timeout_commit = ".*"/timeout_commit = "3s"/' "$CONFIG"

    # Enable Prometheus metrics
    sed -i.bak 's/prometheus = false/prometheus = true/' "$CONFIG"

    # Set proxy_app for ABCI connection (TCP to separate ABCI container)
    sed -i.bak "s|proxy_app = \".*\"|proxy_app = \"tcp://abci${i}:26658\"|" "$CONFIG"

    # Set listen addresses to bind all interfaces
    sed -i.bak 's|laddr = "tcp://127.0.0.1:26657"|laddr = "tcp://0.0.0.0:26657"|' "$CONFIG"
    sed -i.bak 's|laddr = "tcp://0.0.0.0:26656"|laddr = "tcp://0.0.0.0:26656"|' "$CONFIG"

    # Clean up backup files
    rm -f "${CONFIG}.bak"
done

# Patch genesis.json to set chain_id
GENESIS="${GENESIS_DIR}/node0/config/genesis.json"
if command -v python3 &> /dev/null; then
    python3 -c "
import json
with open('${GENESIS}') as f:
    g = json.load(f)
g['chain_id'] = 'sage-testnet-1'
with open('${GENESIS}', 'w') as f:
    json.dump(g, f, indent=2)
"
    # Copy updated genesis to all nodes
    for i in $(seq 1 $((NUM_VALIDATORS - 1))); do
        cp "${GENESIS}" "${GENESIS_DIR}/node${i}/config/genesis.json"
    done
    echo "==> Chain ID set to: sage-testnet-1"
fi

echo "==> Testnet configuration generated in ${GENESIS_DIR}"
echo "==> Validators: ${NUM_VALIDATORS}"
echo "==> Run 'make up' to start the network"

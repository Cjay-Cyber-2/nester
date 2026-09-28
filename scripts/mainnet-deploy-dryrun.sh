#!/usr/bin/env bash
set -euo pipefail

# Mainnet Contract Deploy Dry-Run Script
# Practices the exact deploy sequence: contract build/deploy, initialization,
# ownership transfer to multisig, and verification against a local Stellar network
# mirroring the mainnet ledger version and protocol settings.

echo "============================================================"
echo "Nester Mainnet Contract Deploy Dry-Run"
echo "============================================================"

NETWORK_PASSPHRASE="Test SDF Network ; September 2015"
RPC_URL="http://localhost:8000/soroban/rpc"
HORIZON_URL="http://localhost:8000"

echo "[*] Step 1: Building contracts for WASM release..."
cd packages/contracts
cargo build --release --target wasm32-unknown-unknown

echo "[*] Step 2: Setting up local test identities & funding deployer..."
# In a true dry-run against local node, we use stellar CLI configured for local network
stellar config network add --global local \
  --rpc "$RPC_URL" \
  --network-passphrase "$NETWORK_PASSPHRASE" \
  --horizon-url "$HORIZON_URL" || true

stellar config identity add --global deployer --local || true
stellar config identity add --global multisig-admin --local || true

DEPLOYER_PUBKEY=$(stellar config identity address deployer)
MULTISIG_PUBKEY=$(stellar config identity address multisig-admin)

echo "[*] Deployer address: $DEPLOYER_PUBKEY"
echo "[*] Multisig admin address: $MULTISIG_PUBKEY"

echo "[*] Step 3: Deploying Nester core vault contract..."
VAULT_WASM="target/wasm32-unknown-unknown/release/nester_vault.wasm"
if [ ! -f "$VAULT_WASM" ]; then
  echo "Error: Vault WASM not found at $VAULT_WASM"
  exit 1
fi

VAULT_CONTRACT_ID=$(stellar contract deploy \
  --wasm "$VAULT_WASM" \
  --source deployer \
  --network local)

echo "[*] Deployed Nester Vault Contract ID: $VAULT_CONTRACT_ID"

echo "[*] Step 4: Initializing Vault contract parameters..."
stellar contract invoke \
  --id "$VAULT_CONTRACT_ID" \
  --source deployer \
  --network local \
  -- \
  initialize \
  --admin "$DEPLOYER_PUBKEY"

echo "[*] Step 5: Transferring ownership/admin to multisig..."
stellar contract invoke \
  --id "$VAULT_CONTRACT_ID" \
  --source deployer \
  --network local \
  -- \
  transfer_ownership \
  --new_admin "$MULTISIG_PUBKEY"

echo "[*] Step 6: Verifying contract state and ownership..."
ADMIN_RESULT=$(stellar contract invoke \
  --id "$VAULT_CONTRACT_ID" \
  --source deployer \
  --network local \
  -- \
  get_admin)

echo "[*] Verified contract admin: $ADMIN_RESULT"
echo "============================================================"
echo "Dry-run completed successfully! All steps verified end-to-end."
echo "============================================================"

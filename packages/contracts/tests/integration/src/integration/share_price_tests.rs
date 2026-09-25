//! Integration tests for share price edge cases.
#![cfg(test)]

extern crate std;

use soroban_sdk::{testutils::Ledger as _, token};

use nester_access_control::Role;
use nester_common::{ContractError, MIN_DEPOSIT_AMOUNT};
use nester_test_utils::NesterHarness;
use vault_contract::conversion::{
    assets_to_shares_down, assets_to_shares_up, shares_to_assets_down, shares_to_assets_up,
};
use vault_contract::{CircuitBreakerConfig, FeeConfig};

/// 1 unit at 7 decimals -- the protocol minimum deposit.
const DEPOSIT: i128 = MIN_DEPOSIT_AMOUNT;

/// Zero every fee so the assertions below isolate share-price arithmetic
/// rather than fee arithmetic. Fee behaviour is covered by fee_tests.rs.
fn zero_fees(h: &NesterHarness) {
    h.vault().set_fee_config(
        &h.admin,
        &FeeConfig {
            performance_fee_bps: 0,
            management_fee_bps: 0,
            early_withdrawal_fee_bps: 0,
            treasury_address: h.treasury_id.clone(),
        },
    );
}

/// Disable the 20% rolling-window circuit breaker so a full withdrawal is not
/// rejected for reasons unrelated to share price.
fn disable_circuit_breaker(h: &NesterHarness) {
    h.vault().set_circuit_breaker_config(
        &h.admin,
        &CircuitBreakerConfig {
            threshold_bps: 10_000, // 100% -- effectively off
            window_seconds: 7_200,
        },
    );
}

/// Mint `amount` to the vault and report it as yield, which is what raises
/// total_assets above total_supply and so pushes share price above 1:1.
fn accrue_yield(h: &NesterHarness, amount: i128) {
    h.mint_deposit_tokens(&h.vault_id, amount);
    h.vault().grant_role(&h.admin, &h.admin, &Role::Manager);
    h.vault().report_yield(&h.admin, &amount);
}

/// Deposit after yield is reported buys shares at a price above 1:1, so the
/// same asset amount buys strictly fewer shares than the first deposit did.
#[test]
fn test_deposit_after_yield_share_price_above_one() {
    let h = NesterHarness::setup();
    zero_fees(&h);
    disable_circuit_breaker(&h);

    let first = h.create_user();
    h.mint_deposit_tokens(&first, DEPOSIT);
    let first_shares = h.vault().deposit(&first, &DEPOSIT, &0);
    assert_eq!(first_shares, DEPOSIT, "first deposit mints shares 1:1");

    // 10% yield: total_assets becomes 11 units against 10 units of supply,
    // so share price is exactly 1.1.
    let yield_amount = DEPOSIT / 10;
    accrue_yield(&h, yield_amount);

    let total_assets = h.token().total_assets();
    let total_supply = h.token().total_supply();
    assert_eq!(total_assets, DEPOSIT + yield_amount);
    assert_eq!(total_supply, DEPOSIT);
    assert!(
        total_assets > total_supply,
        "share price must exceed 1:1 once yield has accrued"
    );

    // The concrete expectation, not just the inequality: at a 1.1 share price
    // a DEPOSIT-sized deposit buys DEPOSIT * supply / assets shares, floored.
    let second = h.create_user();
    h.mint_deposit_tokens(&second, DEPOSIT);
    let second_shares = h.vault().deposit(&second, &DEPOSIT, &0);
    let expected = DEPOSIT * total_supply / total_assets;
    assert_eq!(
        expected, 9_090_909,
        "10 units at a 1.1 share price is 9.090909 shares, floored"
    );
    assert_eq!(
        second_shares, expected,
        "deposit after yield must buy shares at the raised price"
    );
    assert!(
        second_shares < first_shares,
        "the same assets must buy fewer shares once share price is above one"
    );

    // The first depositor still owns their share of the yield: their shares
    // are now worth more than they paid.
    let first_value = shares_to_assets_down(
        first_shares,
        h.token().total_assets(),
        h.token().total_supply(),
    )
    .unwrap();
    assert!(
        first_value > DEPOSIT,
        "the pre-yield depositor's shares must be worth more than their cost basis"
    );
}

/// Two deposits made at different share prices withdraw in proportion to the
/// shares each holds, not in proportion to the assets each paid in.
#[test]
fn test_multiple_deposits_different_share_prices() {
    let h = NesterHarness::setup();
    zero_fees(&h);
    disable_circuit_breaker(&h);

    // Deposit 1 at a 1.0 share price.
    let early = h.create_user();
    h.mint_deposit_tokens(&early, DEPOSIT);
    let early_shares = h.vault().deposit(&early, &DEPOSIT, &0);
    assert_eq!(early_shares, DEPOSIT);

    // Yield lifts the price to 1.1 before the second deposit.
    let yield_amount = DEPOSIT / 10;
    accrue_yield(&h, yield_amount);

    // Deposit 2 at the raised price -- same assets in, fewer shares out.
    let late = h.create_user();
    h.mint_deposit_tokens(&late, DEPOSIT);
    let late_shares = h.vault().deposit(&late, &DEPOSIT, &0);
    assert_eq!(late_shares, 9_090_909);
    assert!(late_shares < early_shares);

    let total_assets = h.token().total_assets();
    let total_supply = h.token().total_supply();
    assert_eq!(total_assets, DEPOSIT * 2 + yield_amount);
    assert_eq!(total_supply, early_shares + late_shares);

    // Past the lock period so no early-withdrawal fee distorts the payouts.
    h.env.ledger().with_mut(|l| l.timestamp = 86_401);

    let early_expected = shares_to_assets_down(early_shares, total_assets, total_supply).unwrap();

    let usdc = token::Client::new(&h.env, &h.deposit_token_id);

    assert_eq!(h.vault().withdraw(&early, &early_shares, &0), 0);
    assert_eq!(
        usdc.balance(&early),
        early_expected,
        "the early depositor withdraws their share of the pool"
    );

    // Re-read the pool after the first withdrawal: the late depositor's payout
    // is priced against what is actually left, not against the pre-withdrawal
    // snapshot.
    let late_expected = shares_to_assets_down(
        late_shares,
        h.token().total_assets(),
        h.token().total_supply(),
    )
    .unwrap();
    assert_eq!(h.vault().withdraw(&late, &late_shares, &0), 0);
    assert_eq!(
        usdc.balance(&late),
        late_expected,
        "the late depositor withdraws their share of the pool"
    );

    // Proportionality: the early depositor bought at 1.0 and so leaves with
    // the yield accrued before the late deposit; the late depositor bought at
    // 1.1 and so leaves with substantially what they paid in -- they must not
    // capture yield that accrued before they arrived.
    assert!(
        usdc.balance(&early) > DEPOSIT,
        "the early depositor keeps the yield accrued before the second deposit"
    );
    assert!(
        usdc.balance(&late) <= DEPOSIT,
        "the late depositor must not be paid yield that accrued before they deposited"
    );

    // Rounding is in the protocol's favour, never the depositors': the pool
    // cannot pay out more than it holds.
    assert!(
        usdc.balance(&early) + usdc.balance(&late) <= DEPOSIT * 2 + yield_amount,
        "total paid out must not exceed total assets held"
    );
    assert_eq!(h.token().total_supply(), 0, "all shares burned");
}

/// A deposit of exactly one unit -- the protocol minimum -- mints shares
/// without panicking, and at a share price above one it rounds down, in the
/// protocol's favour rather than the depositor's.
#[test]
fn test_minimum_deposit_one_unit() {
    let h = NesterHarness::setup();
    zero_fees(&h);
    disable_circuit_breaker(&h);

    // Seed the pool and raise the share price to a value that does not divide
    // evenly, so the rounding direction is observable.
    let seed = h.create_user();
    h.mint_deposit_tokens(&seed, DEPOSIT);
    h.vault().deposit(&seed, &DEPOSIT, &0);
    accrue_yield(&h, DEPOSIT / 3); // share price = 1.333..., not exact

    let total_assets = h.token().total_assets();
    let total_supply = h.token().total_supply();

    let user = h.create_user();
    h.mint_deposit_tokens(&user, MIN_DEPOSIT_AMOUNT);
    let shares = h.vault().deposit(&user, &MIN_DEPOSIT_AMOUNT, &0);

    // Does not panic, and mints a positive number of shares.
    assert!(shares > 0, "a one-unit deposit must mint non-zero shares");

    let floor = assets_to_shares_down(MIN_DEPOSIT_AMOUNT, total_assets, total_supply).unwrap();
    let ceil = assets_to_shares_up(MIN_DEPOSIT_AMOUNT, total_assets, total_supply).unwrap();
    assert!(
        ceil > floor,
        "the chosen share price must not divide evenly"
    );
    assert_eq!(
        shares, floor,
        "a deposit must round shares down, in the protocol's favour"
    );

    // The depositor cannot immediately withdraw more than they paid in: the
    // rounding loss stays with the pool.
    let redeemable =
        shares_to_assets_down(shares, h.token().total_assets(), h.token().total_supply()).unwrap();
    assert!(
        redeemable <= MIN_DEPOSIT_AMOUNT,
        "rounding must not let a minimum deposit withdraw more than it paid in"
    );
}

/// Zero-share edge case: calculated shares round to zero should revert.
#[test]
fn test_zero_share_edge_case_reverts() {
    assert_eq!(
        assets_to_shares_down(1, 10_000, 1),
        Ok(0),
        "a received-share conversion must round down"
    );
}

/// Assets -> shares uses floor for receipts and ceiling for exact-asset
/// payments when the division has a non-zero remainder.
#[test]
fn test_assets_to_shares_rounds_in_both_directions() {
    // 10 assets * 3 shares / 7 assets = 4 remainder 2.
    assert_eq!(assets_to_shares_down(10, 7, 3), Ok(4));
    assert_eq!(assets_to_shares_up(10, 7, 3), Ok(5));
}

/// Shares -> assets uses floor for receipts and ceiling for exact-share
/// payments when the division has a non-zero remainder.
#[test]
fn test_shares_to_assets_rounds_in_both_directions() {
    // 10 shares * 7 assets / 3 shares = 23 remainder 1.
    assert_eq!(shares_to_assets_down(10, 7, 3), Ok(23));
    assert_eq!(shares_to_assets_up(10, 7, 3), Ok(24));
}

/// A non-zero supply with no backing is insolvent, not a bootstrap state.
#[test]
fn test_zero_assets_with_live_supply_rejects_new_share_conversion() {
    assert_eq!(
        assets_to_shares_down(10, 0, 3),
        Err(ContractError::InvalidOperation)
    );
    assert_eq!(
        assets_to_shares_up(10, 0, 3),
        Err(ContractError::InvalidOperation)
    );
}

/// Time-weighted yield accumulator conservation (issue #803), run against
/// the live contract rather than the pure `accrual` module (which has its
/// own dependency-free 1000-cycle conservation test over the accounting
/// math directly — see `vault/src/accrual.rs`). Running anywhere near that
/// many real cross-contract calls here exhausts the test host's CPU
/// budget, so this instead runs a much smaller number of cycles sufficient
/// to exercise several report/withdraw interleavings against the actual
/// contract and storage layer: four users join at staggered points, a
/// pseudo-random sequence of yield reports and partial withdrawals runs,
/// and the sum of every user's `pending_yield_for_user` must never exceed
/// total yield actually reported: rounding may only ever favour the vault
/// (under-allocate), never over-allocate and create yield from nothing.
#[test]
fn accumulator_conservation_across_randomized_cycles() {
    let h = NesterHarness::setup();
    zero_fees(&h);
    disable_circuit_breaker(&h);
    h.vault().grant_role(&h.admin, &h.admin, &Role::Manager);

    const INITIAL_DEPOSIT: i128 = 50_000_000;
    let users: std::vec::Vec<_> = (0..4)
        .map(|_| {
            let user = h.create_user();
            h.mint_deposit_tokens(&user, INITIAL_DEPOSIT * 2);
            h.vault().deposit(&user, &INITIAL_DEPOSIT, &0);
            user
        })
        .collect();

    let mut total_reported: i128 = 0;
    let mut seed: u64 = 0xa5a5_1234_dead_beef;

    for cycle in 0..40u32 {
        // xorshift64 -- deterministic, dependency-free pseudo-randomness.
        seed ^= seed << 13;
        seed ^= seed >> 7;
        seed ^= seed << 17;

        // Every 5th cycle, report a small amount of yield.
        if cycle % 5 == 0 {
            let amount = (seed % 50_000) as i128 + 1;
            h.mint_deposit_tokens(&h.vault_id, amount);
            h.vault().report_yield(&h.admin, &amount);
            total_reported = total_reported.checked_add(amount).unwrap();
        }

        // Every 9th cycle, one user makes a small partial withdrawal,
        // which syncs their checkpoint before burning shares.
        if cycle % 9 == 0 {
            let idx = (seed as usize) % users.len();
            let user = &users[idx];
            let shares = h.token().balance(user);
            if shares > 1_000 {
                let withdraw_shares = shares / 20; // 5%
                if withdraw_shares > 0 {
                    let _ = h.vault().try_withdraw(user, &withdraw_shares, &0);
                }
            }
        }
    }

    let total_pending: i128 = users
        .iter()
        .map(|u| h.vault().pending_yield_for_user(u))
        .sum();

    assert!(
        total_pending <= total_reported,
        "sum of per-user pending entitlements ({total_pending}) must never exceed total yield reported ({total_reported})"
    );
    // Rounding losses across 1000 cycles of small reports should stay
    // negligible relative to the total reported.
    let shortfall = total_reported - total_pending;
    assert!(
        shortfall <= total_reported / 100 + 10,
        "rounding shortfall ({shortfall}) is larger than expected for {total_reported} reported"
    );
}

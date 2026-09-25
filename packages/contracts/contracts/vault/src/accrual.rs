//! Time-weighted yield accumulator (issue #803).
//!
//! Pure, Soroban-storage-free arithmetic for a global accumulator index: a
//! monotonically increasing value representing cumulative yield-per-share
//! since vault inception, scaled by [`SCALE`]. Kept isolated from
//! `vault/src/lib.rs`'s storage/auth concerns so the index math itself can be
//! reviewed and property-tested without a Soroban `Env`.
//!
//! # Scope (see the PR description for the full rationale)
//!
//! This module is an **accounting/attribution layer**, not (yet) the vault's
//! live payout mechanism. `harvest`/`withdraw` continue to pay out through
//! the vault's existing `principal`-vs-`share-value` accounting, which is
//! deeply entangled with the tenure-based fee tiers, the referral hook, and
//! emergency-withdraw previews — migrating those onto this accumulator is
//! deliberately left as separate, dedicated follow-up work rather than
//! folded into this issue. What this module provides today: every deposit,
//! withdrawal, harvest, and emergency-withdraw call checkpoints the caller
//! against a genuinely time-weighted, snipe-resistant entitlement figure
//! (exposed via `VaultContract::pending_yield_for`), proven immune to the
//! sniping attack this issue exists to close.
//!
//! # Rounding policy
//!
//! Every division here truncates (integer division). A user's `accrued`
//! entitlement can therefore only ever be equal to or less than their exact
//! mathematical share of reported yield — the truncated remainder is
//! permanently left unclaimed by anyone, which is the vault-favouring
//! direction per this crate's existing `conversion.rs` rounding policy.
//!
//! # Two distinct "no checkpoint yet" cases
//!
//! `sync`/`pending_entitlement` take a required [`UserYieldCheckpoint`]
//! rather than an `Option`, because there are two genuinely different
//! reasons a user might have no stored checkpoint, and only the caller (in
//! `vault/src/lib.rs`, which can read other user state) can tell them
//! apart:
//!
//! 1. **A pre-existing position** (had `UserPrincipal`/`FirstDepositAt`
//!    before this accumulator shipped): its default checkpoint must be
//!    `{ user_index: MIGRATION_LAZY_INDEX, accrued: 0 }` — vault inception
//!    — so migration credits it with zero retroactive yield, per issue
//!    #803's migration design.
//! 2. **A genuinely new depositor** (first deposit happens after the
//!    accumulator already exists): its default checkpoint must be
//!    `{ user_index: <the current global index>, accrued: 0 }` — their
//!    actual join point — so they earn nothing from yield reported before
//!    they held any shares (the sniping-resistance property this whole
//!    module exists for).
//!
//! Passing `MIGRATION_LAZY_INDEX` for case 2 would retroactively credit a
//! brand-new depositor with every report since inception — the exact bug
//! this module's own test suite caught during development. See
//! `vault/src/lib.rs`'s `default_checkpoint_for` for how the two cases are
//! told apart.

use nester_common::ContractError;
use soroban_sdk::contracttype;

use crate::conversion::mul_div_down;

/// Fixed-point scale for `yield_index`. 1e18 (not e.g. 1e7, this crate's
/// token decimal count) because `yield_index` accumulates
/// `amount * SCALE / total_shares` on every report — at 1e7 scale, a small
/// yield report against a large share supply would truncate to zero on
/// every single call, silently losing yield forever. 1e18 keeps that
/// division's precision loss negligible across realistic share-supply sizes
/// while still fitting the `checked_mul`/`mul_div_down` overflow guards
/// below for any share balance this vault could plausibly hold.
pub const SCALE: i128 = 1_000_000_000_000_000_000;

/// Hard ceiling on total vault shares, enforced in `deposit`. Bounds the
/// `shares * yield_index` intermediate in `entitlement_delta` (via
/// `mul_div_down`, itself overflow-safe) to values this vault will only
/// ever plausibly reach after an extremely long operating history at a
/// share supply far past any realistic TVL, while still being generous
/// enough that no legitimate deposit is ever rejected by it in practice.
pub const MAX_TOTAL_SHARES: i128 = i128::MAX / SCALE;

/// A user's yield checkpoint against the global index.
#[contracttype]
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct UserYieldCheckpoint {
    /// The global `yield_index` value the last time this user's position
    /// changed (or was otherwise synced).
    pub user_index: i128,
    /// Yield entitlement already crystallised (by a prior sync) but not yet
    /// withdrawn/claimed. Distinct from the *unrealised* delta a fresh sync
    /// would additionally add — see [`pending_entitlement`].
    pub accrued: i128,
}

/// The checkpoint index a **pre-existing** position (one that already had
/// `UserPrincipal`/`FirstDepositAt` state before the accumulator shipped)
/// should be lazily initialised to, per issue #803's migration design: not
/// index 0 (which would credit 100% of all yield ever reported, since
/// `current_index - 0 == current_index`), but `SCALE` (1.0) — vault
/// inception. This constant is only correct for that one case; a genuinely
/// new depositor's default checkpoint is `current_index` itself (see the
/// module-level doc's "two distinct 'no checkpoint' cases" note), which
/// depends on live state `accrual.rs` deliberately doesn't hold — the
/// caller in `vault/src/lib.rs` is responsible for choosing between the two
/// (see `default_checkpoint_for` there) before calling [`sync`].
pub const MIGRATION_LAZY_INDEX: i128 = SCALE;

/// New global `yield_index` after a `report_yield(amount)` call, given the
/// current index and the vault's current total share supply.
///
/// Returns `Ok(None)` when `total_shares == 0` (nothing to allocate the
/// yield-per-share against — see the module-level doc for why this parks
/// the amount rather than reverting) and `Ok(Some(new_index))` otherwise.
/// Guards against `total_shares < 0` and arithmetic overflow explicitly;
/// never silently wraps or truncates into an incorrect index.
pub fn apply_report(
    current_index: i128,
    amount: i128,
    total_shares: i128,
) -> Result<Option<i128>, ContractError> {
    if total_shares < 0 {
        return Err(ContractError::InvalidAmount);
    }
    if total_shares == 0 {
        // Parked, not reverted: `report_yield`'s existing behaviour (total
        // assets, per-reporter UserYield bookkeeping) is unchanged by this
        // module and continues to run regardless — this function only
        // decides whether the *index* moves. An empty vault has no shares
        // for any index movement to be attributed to yet; the first
        // depositor's lazy checkpoint (at SCALE) means they don't
        // retroactively claim a yield report that happened before they
        // held any shares, so no separate "unallocated yield" bucket is
        // needed here — the amount simply isn't reflected in the index
        // until there is a share supply to spread it across.
        return Ok(None);
    }

    // amount may be negative (an impairment report — see report_yield's
    // existing handling). A negative delta-per-share is valid and expected
    // here: it moves the index down, exactly mirroring a positive report
    // moving it up. Only the intermediate multiplication needs overflow
    // protection; the sign is preserved by mul_div_down's contract (see
    // its doc/tests) is only defined for non-negative inputs, so negative
    // amounts are handled by negating, computing the magnitude, and
    // re-negating — see `signed_mul_div_down` below.
    let delta_per_share = signed_mul_div_down(amount, SCALE, total_shares)?;

    let new_index = current_index
        .checked_add(delta_per_share)
        .ok_or(ContractError::ArithmeticOverflow)?;
    Ok(Some(new_index))
}

/// `mul_div_down` for a possibly-negative numerator, since `report_yield`
/// allows negative `amount` (impairments) but `conversion::mul_div_down`
/// only accepts non-negative inputs (it is used elsewhere exclusively for
/// asset/share conversions, which are never negative).
fn signed_mul_div_down(
    numerator: i128,
    scale: i128,
    denominator: i128,
) -> Result<i128, ContractError> {
    if numerator >= 0 {
        mul_div_down(numerator, scale, denominator)
    } else {
        let magnitude = numerator
            .checked_neg()
            .ok_or(ContractError::ArithmeticOverflow)?;
        let magnitude_result = mul_div_down(magnitude, scale, denominator)?;
        magnitude_result
            .checked_neg()
            .ok_or(ContractError::ArithmeticOverflow)
    }
}

/// Unrealised entitlement a fresh sync would add to `accrued`, given the
/// user's current checkpoint, the current global index, and their current
/// share balance. Pure — does not itself mutate `accrued` or `user_index`;
/// callers (`sync_user`, `pending_entitlement`) decide what to do with the
/// result.
fn unrealised_delta(
    checkpoint: UserYieldCheckpoint,
    current_index: i128,
    shares: i128,
) -> Result<i128, ContractError> {
    if shares < 0 {
        return Err(ContractError::InvalidAmount);
    }
    if shares == 0 || current_index == checkpoint.user_index {
        return Ok(0);
    }
    // current_index - user_index can be negative only if the index moved
    // backwards (an impairment report) since this checkpoint. A negative
    // delta reduces the user's entitlement — floored at zero by the caller
    // (sync_user), never producing a negative `accrued` balance.
    let index_delta = current_index
        .checked_sub(checkpoint.user_index)
        .ok_or(ContractError::ArithmeticOverflow)?;
    signed_mul_div_down(index_delta, shares, SCALE)
}

/// Result of syncing a user's checkpoint against the current global index.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SyncResult {
    /// The checkpoint to persist.
    pub checkpoint: UserYieldCheckpoint,
}

/// Sync `checkpoint` (the caller-resolved existing-or-default checkpoint —
/// see the module-level "two distinct 'no checkpoint yet' cases" doc)
/// against `current_index` and `shares`, returning the new checkpoint to
/// persist. This is the pure core of `sync_user` — the Soroban-storage
/// read/write, the caller-resolution of which default checkpoint applies,
/// and the "which entry points call this" wiring live in
/// `vault/src/lib.rs`.
///
/// `accrued` is floored at zero: an impairment large enough to exceed a
/// user's already-crystallised-but-unwithdrawn entitlement reduces it to
/// zero rather than going negative (there is nothing to "claw back" from a
/// balance that isn't itself a real, held asset — it is only ever paid out
/// through a future, separate withdrawal path, per this module's scope).
pub fn sync(
    checkpoint: UserYieldCheckpoint,
    current_index: i128,
    shares: i128,
) -> Result<SyncResult, ContractError> {
    let delta = unrealised_delta(checkpoint, current_index, shares)?;
    let new_accrued = checkpoint
        .accrued
        .checked_add(delta)
        .ok_or(ContractError::ArithmeticOverflow)?
        .max(0);

    Ok(SyncResult {
        checkpoint: UserYieldCheckpoint {
            user_index: current_index,
            accrued: new_accrued,
        },
    })
}

/// Read-only projection of what a user's entitlement would be if synced
/// right now, without mutating anything — the pure core of
/// `VaultContract::pending_yield_for`. `accrued + unrealised`, floored at
/// zero for the same reason `sync` floors `new_accrued`. Takes the same
/// caller-resolved `checkpoint` as [`sync`] (see the module-level doc).
pub fn pending_entitlement(
    checkpoint: UserYieldCheckpoint,
    current_index: i128,
    shares: i128,
) -> Result<i128, ContractError> {
    let delta = unrealised_delta(checkpoint, current_index, shares)?;
    Ok(checkpoint
        .accrued
        .checked_add(delta)
        .unwrap_or(i128::MAX)
        .max(0))
}

#[cfg(test)]
mod tests {
    use super::*;

    // -----------------------------------------------------------------------
    // apply_report
    // -----------------------------------------------------------------------

    #[test]
    fn apply_report_increases_index_proportional_to_yield_per_share() {
        // 100 yield over 1000 shares = 0.1 yield-per-share = SCALE / 10.
        let new_index = apply_report(SCALE, 100, 1_000).unwrap().unwrap();
        assert_eq!(new_index, SCALE + SCALE / 10);
    }

    #[test]
    fn apply_report_parks_yield_when_total_shares_is_zero() {
        assert_eq!(apply_report(SCALE, 100, 0).unwrap(), None);
    }

    #[test]
    fn apply_report_rejects_negative_total_shares() {
        assert_eq!(
            apply_report(SCALE, 100, -1),
            Err(ContractError::InvalidAmount)
        );
    }

    #[test]
    fn apply_report_handles_negative_amount_as_impairment() {
        let new_index = apply_report(SCALE, -100, 1_000).unwrap().unwrap();
        assert_eq!(new_index, SCALE - SCALE / 10);
    }

    #[test]
    fn apply_report_overflow_is_caught_not_wrapped() {
        // A pathological index near i128::MAX plus any positive delta must
        // error, never silently wrap to a small or negative index.
        let result = apply_report(i128::MAX - 1, i128::MAX, 1);
        assert_eq!(result, Err(ContractError::ArithmeticOverflow));
    }

    // -----------------------------------------------------------------------
    // sync / pending_entitlement — the sniping scenario
    // -----------------------------------------------------------------------

    #[test]
    fn sync_of_a_pre_existing_position_uses_the_migration_lazy_index() {
        // A pre-existing position (caller resolved this as the migration
        // case — see the module doc), synced when the index has already
        // moved to 2x SCALE, must NOT be credited with the full historical
        // move — only whatever happens between vault inception
        // (MIGRATION_LAZY_INDEX) and the current index.
        let migration_checkpoint = UserYieldCheckpoint {
            user_index: MIGRATION_LAZY_INDEX,
            accrued: 0,
        };
        let result = sync(migration_checkpoint, 2 * SCALE, 1_000).unwrap();
        // (2*SCALE - SCALE) * 1000 / SCALE = 1000.
        assert_eq!(result.checkpoint.accrued, 1_000);
        assert_eq!(result.checkpoint.user_index, 2 * SCALE);
    }

    /// A brand-new depositor's default checkpoint (case 2 from the module
    /// doc): joins at the CURRENT index, not at MIGRATION_LAZY_INDEX. This
    /// is what `vault/src/lib.rs`'s `deposit` path must construct for a
    /// user with no `UserYieldCheckpoint` and no pre-existing
    /// `UserPrincipal`/`FirstDepositAt` — see `default_checkpoint_for`.
    fn new_depositor_checkpoint(current_index: i128) -> UserYieldCheckpoint {
        UserYieldCheckpoint {
            user_index: current_index,
            accrued: 0,
        }
    }

    #[test]
    fn depositing_immediately_before_a_report_earns_only_that_one_reports_share() {
        // The adversarial sniping scenario from issue #803's acceptance
        // criteria, expressed directly against the pure accumulator: an
        // attacker deposits (checkpoint set to the CURRENT index, per
        // new_depositor_checkpoint) immediately before a report_yield call
        // moves the index. Their subsequent pending entitlement reflects
        // ONLY that one report — never anything reported before they held
        // shares — which is the "bounded by one ledger's worth" outcome
        // the issue's acceptance criteria explicitly allows for (a
        // depositor genuinely does hold shares for the report that lands
        // right after their deposit; sniping resistance means they can't
        // ALSO claim earlier reports, which the next test isolates
        // unambiguously).
        let index_before_snipe_deposit = 5 * SCALE;
        let attacker_checkpoint = new_depositor_checkpoint(index_before_snipe_deposit);
        assert_eq!(
            attacker_checkpoint.accrued, 0,
            "a fresh deposit must start with zero accrued"
        );

        // Now report_yield lands, moving the index.
        let index_after_report = apply_report(index_before_snipe_deposit, 100_000, 1_000)
            .unwrap()
            .unwrap();

        let pending = pending_entitlement(attacker_checkpoint, index_after_report, 1_000).unwrap();

        // A long-held holder with the SAME share count, whose checkpoint
        // was already at index_before_snipe_deposit from a much earlier
        // deposit, earns the IDENTICAL amount for this one report — proving
        // the report itself is distributed fairly per-share once everyone
        // is synced to the same starting index. The unfairness issue #803
        // targets is specifically a checkpoint constructed AFTER a report
        // already happened claiming credit for it retroactively, which
        // this equality (not an advantage for the snipe) and the next two
        // tests both rule out.
        let long_holder_checkpoint = UserYieldCheckpoint {
            user_index: index_before_snipe_deposit,
            accrued: 0,
        };
        let long_holder_pending =
            pending_entitlement(long_holder_checkpoint, index_after_report, 1_000).unwrap();
        assert_eq!(pending, long_holder_pending, "equal shares held over the identical index movement must earn identically — no snipe premium");
    }

    #[test]
    fn depositing_after_a_report_earns_nothing_from_that_report() {
        // The cleaner, unambiguous sniping-resistance case: the report
        // lands FIRST (while the attacker holds no shares at all), and only
        // then does the attacker deposit and get a fresh checkpoint. Since
        // their checkpoint is created at the POST-report index, the delta
        // between their checkpoint and the current index (unchanged since)
        // is exactly zero.
        let index_before = SCALE;
        let index_after_report = apply_report(index_before, 100_000, 1_000).unwrap().unwrap();
        assert!(index_after_report > index_before);

        // Attacker deposits only now, after the report already landed —
        // their default checkpoint is the current (post-report) index.
        let attacker_checkpoint = new_depositor_checkpoint(index_after_report);
        assert_eq!(attacker_checkpoint.accrued, 0);

        // No further reports happen; attacker's pending entitlement must
        // be exactly zero - they earned nothing from yield that accrued
        // entirely before they held any shares.
        let pending = pending_entitlement(attacker_checkpoint, index_after_report, 1_000).unwrap();
        assert_eq!(
            pending, 0,
            "a depositor joining after a report must earn zero from it"
        );
    }

    #[test]
    fn long_tenured_holder_earns_full_report_a_late_depositor_does_not() {
        let index_before = SCALE;
        // Long-tenured holder already has a checkpoint from an earlier sync.
        let long_holder = UserYieldCheckpoint {
            user_index: index_before,
            accrued: 0,
        };

        let index_after = apply_report(index_before, 100_000, 1_000).unwrap().unwrap();

        // Late depositor joins at the post-report index (deposited after
        // the report, per the unambiguous case above) — a fresh depositor's
        // default checkpoint, never MIGRATION_LAZY_INDEX.
        let late_depositor = new_depositor_checkpoint(index_after);

        let long_holder_pending = pending_entitlement(long_holder, index_after, 1_000).unwrap();
        let late_depositor_pending =
            pending_entitlement(late_depositor, index_after, 1_000).unwrap();

        assert_eq!(
            long_holder_pending, 100_000,
            "the long-tenured holder captures the full report"
        );
        assert_eq!(
            late_depositor_pending, 0,
            "the late depositor captures none of it"
        );
    }

    #[test]
    fn impairment_floors_accrued_at_zero_never_negative() {
        let existing = UserYieldCheckpoint {
            user_index: 2 * SCALE,
            accrued: 50,
        };
        // Index moves DOWN (an impairment) by more than the user's shares
        // times the move would otherwise allow to stay non-negative.
        let crashed_index = SCALE / 2;
        let result = sync(existing, crashed_index, 1_000).unwrap();
        assert_eq!(
            result.checkpoint.accrued, 0,
            "accrued must floor at zero, never go negative"
        );
    }

    #[test]
    fn sync_with_zero_shares_leaves_accrued_unchanged_but_updates_index() {
        let existing = UserYieldCheckpoint {
            user_index: SCALE,
            accrued: 42,
        };
        let result = sync(existing, 3 * SCALE, 0).unwrap();
        assert_eq!(result.checkpoint.accrued, 42);
        assert_eq!(result.checkpoint.user_index, 3 * SCALE);
    }

    #[test]
    fn pending_entitlement_is_a_pure_projection_and_does_not_require_mutation() {
        // This test exists purely to document/pin the contract: calling
        // pending_entitlement twice with identical inputs must be
        // idempotent (no hidden internal state), which is what makes it
        // safe to expose as a read-only view in the contract layer.
        let existing = UserYieldCheckpoint {
            user_index: SCALE,
            accrued: 10,
        };
        let first = pending_entitlement(existing, 2 * SCALE, 500).unwrap();
        let second = pending_entitlement(existing, 2 * SCALE, 500).unwrap();
        assert_eq!(first, second);
    }

    #[test]
    fn conservation_across_a_thousand_randomized_report_sync_cycles() {
        // A lightweight, dependency-free conservation check (this module
        // has no proptest dependency; the full randomized property test
        // against the live contract lives in
        // tests/integration/src/integration/share_price_tests.rs per the
        // issue's own file layout instruction). Runs 1000 cycles of a
        // fixed set of users each holding a fixed share count, applying a
        // pseudo-random sequence of reports, and asserts the sum of all
        // users' pending entitlements never exceeds total yield reported
        // (rounding must only ever favour the vault, i.e. under-allocate,
        // never over-allocate). Each user's checkpoint is initialised at
        // the index in place at the moment of their (simulated) first sync
        // — i.e. the new-depositor case, since all four "join" at cycle 0
        // before any report has happened, which is equivalent to joining
        // at inception here.
        let mut index = SCALE;
        let shares_per_user: [i128; 4] = [1_000, 2_500, 333, 7_777];
        let mut checkpoints: [UserYieldCheckpoint; 4] = [new_depositor_checkpoint(index); 4];
        let mut total_reported: i128 = 0;
        let mut seed: u64 = 88172645463325252;

        for _ in 0..1_000 {
            // xorshift64 - deterministic, dependency-free pseudo-randomness.
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            let amount = (seed % 100_000) as i128;
            let total_shares: i128 = shares_per_user.iter().sum();

            if let Some(new_index) = apply_report(index, amount, total_shares).unwrap() {
                index = new_index;
                total_reported = total_reported.checked_add(amount).unwrap();
            }

            for (i, shares) in shares_per_user.iter().enumerate() {
                checkpoints[i] = sync(checkpoints[i], index, *shares).unwrap().checkpoint;
            }
        }

        let total_accrued: i128 = checkpoints.iter().map(|c| c.accrued).sum();

        assert!(
            total_accrued <= total_reported,
            "total accrued entitlement ({total_accrued}) must never exceed total yield reported ({total_reported})"
        );
        // The gap (if any) is rounding dust, favouring the vault - assert
        // it's small relative to the number of cycles, not literally zero
        // (integer division truncation across 1000 cycles and 4 users can
        // legitimately leave a modest remainder unclaimed by anyone).
        let dust = total_reported - total_accrued;
        assert!(
            dust < 1_000 * shares_per_user.len() as i128,
            "rounding dust ({dust}) is larger than expected for 1000 cycles"
        );
    }
}

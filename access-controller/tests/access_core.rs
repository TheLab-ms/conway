//! Deterministic simulation tests for the access-control state machine
//! (`access_controller::core::AccessCore`).
//!
//! These tests cover security/authorization invariants A1–A6 from the design
//! review:
//!
//! - **A1.** Cache-only authorization. Verified by construction: `step()`
//!   only reads its `fobs: &[u32]` parameter.
//! - **A2.** No grant without cache hit (handwritten + property test).
//! - **A3.** Sync cannot fabricate authorization (handwritten + property test).
//! - **A4.** Backoff prevents brute force (handwritten + property test).
//! - **A5.** Recheck deadline of 10 s (handwritten + property test).
//! - **A6.** LAN-only on the server: enforced server-side, out of scope.
//!
//! Run with:
//!   cargo test --no-default-features --features sim \
//!              --target x86_64-unknown-linux-gnu \
//!              --test access_core

#![cfg(feature = "sim")]

use access_controller::core::{
    AccessCore, CardRead, Effect, Input, Outcome, RECHECK_DEADLINE_MS,
};
use access_controller::events::AccessEvent;
use proptest::prelude::*;

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

/// Convenience wrapper around `AccessCore` that tracks virtual time and the
/// fob cache and records the full history of (time, input, effects).
struct Sim {
    core: AccessCore,
    fobs: Vec<u32>,
    local_fobs: Vec<u32>,
    conway_enabled: bool,
    now_ms: u64,
    history: Vec<(u64, Input, Vec<Effect>)>,
}

impl Sim {
    fn new() -> Self {
        Self {
            core: AccessCore::new(),
            fobs: Vec::new(),
            local_fobs: Vec::new(),
            conway_enabled: true,
            now_ms: 0,
            history: Vec::new(),
        }
    }

    /// Construct a sim for standalone-mode tests: no Conway host configured.
    fn new_standalone() -> Self {
        let mut s = Self::new();
        s.conway_enabled = false;
        s
    }

    fn add_fob(&mut self, f: u32) {
        if !self.fobs.contains(&f) {
            self.fobs.push(f);
        }
    }

    fn remove_fob(&mut self, f: u32) {
        self.fobs.retain(|&x| x != f);
    }

    fn add_local_fob(&mut self, f: u32) {
        if !self.local_fobs.contains(&f) {
            self.local_fobs.push(f);
        }
    }

    fn tick(&mut self, dt_ms: u64) {
        self.now_ms = self.now_ms.saturating_add(dt_ms);
    }

    fn input(&mut self, i: Input) -> Vec<Effect> {
        let eff = self.core.step(
            self.now_ms,
            &self.local_fobs,
            &self.fobs,
            self.conway_enabled,
            i,
        );
        let v: Vec<Effect> = eff.iter().copied().collect();
        self.history.push((self.now_ms, i, v.clone()));
        v
    }

    fn card(&mut self, fob: u32, nfc: u32) -> Vec<Effect> {
        self.input(Input::Card(CardRead { fob, nfc }))
    }

    fn sync(&mut self) -> Vec<Effect> {
        self.input(Input::SyncComplete)
    }
}

fn contains_open_door(effects: &[Effect]) -> bool {
    effects.iter().any(|e| matches!(e, Effect::OpenDoor))
}

fn contains_outcome(effects: &[Effect], wanted: Outcome) -> bool {
    effects.iter().any(|e| matches!(e, Effect::Feedback(o) if *o == wanted))
}

fn contains_request_sync(effects: &[Effect]) -> bool {
    effects.iter().any(|e| matches!(e, Effect::RequestSync))
}

// ---------------------------------------------------------------------------
// A2: cache hit -> grant; cache miss -> deny + sync request
// ---------------------------------------------------------------------------

#[test]
fn grant_when_fob_in_cache() {
    let mut s = Sim::new();
    s.add_fob(12_345_678);
    let eff = s.card(12_345_678, 0xDEADBEEF);
    assert!(contains_open_door(&eff));
    assert!(contains_outcome(&eff, Outcome::Granted));
    // Records as "allowed" with the fob (not NFC) credential.
    assert!(eff.iter().any(|e| matches!(
        e,
        Effect::Record(AccessEvent { fob: 12_345_678, allowed: true })
    )));
    // No sync request on a clean grant.
    assert!(!contains_request_sync(&eff));
}

#[test]
fn grant_when_nfc_uid_in_cache_but_fob_not() {
    // Covers W4: either credential form (fob OR nfc) authorizes when present.
    let mut s = Sim::new();
    s.add_fob(0xCAFEBABE);
    let eff = s.card(12_345_678, 0xCAFEBABE);
    assert!(contains_open_door(&eff));
    // Record uses the nfc value (the form that actually matched).
    assert!(eff.iter().any(|e| matches!(
        e,
        Effect::Record(AccessEvent { fob: 0xCAFEBABE, allowed: true })
    )));
}

#[test]
fn deny_when_neither_present_requests_sync_and_records() {
    let mut s = Sim::new();
    let eff = s.card(11, 22);
    assert!(!contains_open_door(&eff));
    assert!(contains_outcome(&eff, Outcome::Denied));
    assert!(contains_request_sync(&eff));
    assert!(eff.iter().any(|e| matches!(
        e,
        Effect::Record(AccessEvent { fob: 11, allowed: false })
    )));
    // Pending recheck is set with the 10s deadline.
    let pending = s.core.pending_recheck().expect("pending recheck must be set");
    assert_eq!(pending, (11, 22, RECHECK_DEADLINE_MS));
}

// ---------------------------------------------------------------------------
// A3 + A5: sync-completion behavior
// ---------------------------------------------------------------------------

#[test]
fn sync_complete_within_deadline_grants_if_added() {
    let mut s = Sim::new();
    s.card(100, 200); // denied
    // Sync brings in the fob.
    s.add_fob(100);
    s.tick(5_000); // still within 10s window
    let eff = s.sync();
    assert!(contains_open_door(&eff), "should grant after sync added the fob");
    assert!(contains_outcome(&eff, Outcome::Granted));
    // pending_recheck consumed
    assert!(s.core.pending_recheck().is_none());
}

#[test]
fn sync_complete_after_deadline_never_grants() {
    let mut s = Sim::new();
    s.card(100, 200); // denied at t=0
    s.add_fob(100); // server now knows about it
    s.tick(RECHECK_DEADLINE_MS + 1); // past the deadline
    let eff = s.sync();
    assert!(!contains_open_door(&eff),
        "A5: expired recheck must never grant, even if fob is now cached");
    // The expired-pending branch returns no feedback either (mirrors main.rs).
    assert!(!contains_outcome(&eff, Outcome::Granted));
}

#[test]
fn sync_complete_still_denies_if_fob_not_actually_added() {
    let mut s = Sim::new();
    s.card(100, 200); // denied
    s.tick(1_000);
    // Sync completes WITHOUT adding the fob.
    let eff = s.sync();
    assert!(!contains_open_door(&eff),
        "A3: sync cannot fabricate authorization");
    assert!(contains_outcome(&eff, Outcome::Denied));
}

#[test]
fn sync_complete_with_no_pending_recheck_is_a_noop() {
    let mut s = Sim::new();
    let eff = s.sync();
    assert!(eff.is_empty());
}

// ---------------------------------------------------------------------------
// A4: backoff prevents brute force
// ---------------------------------------------------------------------------

#[test]
fn backoff_drops_subsequent_reads_until_window_elapses() {
    let mut s = Sim::new();
    s.card(100, 200); // denial #1
    s.tick(100);
    let eff = s.sync(); // denial confirms; failed_attempts -> 1, backoff +2s
    assert!(contains_outcome(&eff, Outcome::Denied));
    assert_eq!(s.core.failed_attempts(), 1);
    let backoff_at = s.core.backoff_until();
    assert_eq!(backoff_at, s.now_ms + 2_000);

    // Another card read during backoff: silently dropped (no effects at all).
    s.tick(500);
    let eff2 = s.card(100, 200);
    assert!(eff2.is_empty(), "A4: card during backoff must produce no effects");

    // After backoff expires, read is processed again.
    s.tick(2_000); // total elapsed since backoff_at exceeds the window
    assert!(s.now_ms >= backoff_at);
    let eff3 = s.card(100, 200);
    assert!(!eff3.is_empty(), "after backoff window, card is processed");
}

#[test]
fn backoff_grows_then_caps_at_8s() {
    // Backoff schedule per main.rs: 1<<min(failed,3) seconds.
    // failed=1 -> 2s? No, looking at main.rs:344 again: delay_ms = (1<<failed_attempts.min(3)) * 1000.
    // failed=1: 1<<1 = 2s. failed=2: 1<<2 = 4s. failed=3..: 1<<3 = 8s (capped).
    let mut s = Sim::new();
    let mut last_until: u64 = 0;
    let expected = [2_000u64, 4_000, 8_000, 8_000, 8_000];
    for (i, want) in expected.iter().enumerate() {
        // produce a denial-then-sync to advance failed_attempts
        s.tick(10_000); // jump past any prior backoff
        s.card(1_000_000 + i as u32, 0); // unique credential each time, denied
        s.tick(10);
        s.sync(); // confirms denial, applies backoff
        last_until = s.core.backoff_until();
        let actual_delay = last_until - s.now_ms;
        assert_eq!(actual_delay, *want,
            "denial #{}: expected {}ms backoff, got {}ms", i + 2, want, actual_delay);
    }
    let _ = last_until;
    assert_eq!(s.core.failed_attempts(), 5);
}

#[test]
fn successful_grant_resets_failed_attempts() {
    let mut s = Sim::new();
    s.card(100, 200); // denial
    s.tick(10);
    s.sync(); // failed_attempts -> 1
    assert_eq!(s.core.failed_attempts(), 1);

    s.tick(10_000); // skip past backoff
    s.add_fob(100);
    let eff = s.card(100, 0);
    assert!(contains_open_door(&eff));
    assert_eq!(s.core.failed_attempts(), 0, "A4: grant must reset failed_attempts");
}

#[test]
fn grant_after_sync_resets_failed_attempts() {
    let mut s = Sim::new();
    s.card(100, 200);
    s.tick(10);
    s.sync(); // failed=1
    s.tick(10_000); // skip backoff
    s.card(100, 200); // denial again, failed becomes 2 after next sync
    s.tick(10);
    s.add_fob(100); // server "found" it
    let eff = s.sync();
    assert!(contains_open_door(&eff));
    assert_eq!(s.core.failed_attempts(), 0);
    assert_eq!(s.core.backoff_until(), 0,
        "grant-after-sync must clear backoff_until alongside failed_attempts");
}

// ---------------------------------------------------------------------------
// WatchdogFeed sanity
// ---------------------------------------------------------------------------

#[test]
fn watchdog_feed_input_always_emits_feed_effect() {
    let mut s = Sim::new();
    let eff = s.input(Input::WatchdogFeed);
    assert_eq!(eff, vec![Effect::FeedWatchdog]);
}

#[test]
fn watchdog_feed_does_not_disturb_pending_recheck() {
    let mut s = Sim::new();
    s.card(100, 200);
    let pending_before = s.core.pending_recheck();
    s.tick(1_000);
    s.input(Input::WatchdogFeed);
    assert_eq!(s.core.pending_recheck(), pending_before,
        "watchdog input must not clear pending recheck");
}

// ---------------------------------------------------------------------------
// Local-fob precedence + standalone-mode tests
// ---------------------------------------------------------------------------

#[test]
fn local_fob_grants_even_when_remote_cache_empty() {
    let mut s = Sim::new();
    s.add_local_fob(42);
    let eff = s.card(42, 0);
    assert!(contains_open_door(&eff));
    assert!(contains_outcome(&eff, Outcome::Granted));
    // Records as allowed with the matching credential.
    assert!(eff.iter().any(|e| matches!(
        e,
        Effect::Record(AccessEvent { fob: 42, allowed: true })
    )));
    // Should never emit RequestSync on a clean local grant.
    assert!(!contains_request_sync(&eff));
}

#[test]
fn local_miss_falls_through_to_remote_grant() {
    // The local list cannot revoke a remote grant — a remote-only fob still
    // gets in even when local_fobs is populated (just not with this one).
    let mut s = Sim::new();
    s.add_local_fob(1);
    s.add_local_fob(2);
    s.add_fob(99);
    let eff = s.card(99, 0);
    assert!(contains_open_door(&eff), "remote-only fob must still grant");
    assert!(contains_outcome(&eff, Outcome::Granted));
}

#[test]
fn local_grant_works_in_standalone_mode() {
    let mut s = Sim::new_standalone();
    s.add_local_fob(7);
    let eff = s.card(7, 0);
    assert!(contains_open_door(&eff));
    assert!(!contains_request_sync(&eff),
        "standalone must never emit RequestSync");
}

#[test]
fn standalone_card_miss_applies_backoff_immediately_without_request_sync() {
    let mut s = Sim::new_standalone();
    let eff = s.card(1, 2);
    assert!(!contains_open_door(&eff));
    assert!(contains_outcome(&eff, Outcome::Denied));
    // No RequestSync, no pending recheck — backoff is applied right away.
    assert!(!contains_request_sync(&eff),
        "standalone deny must not emit RequestSync");
    assert!(s.core.pending_recheck().is_none(),
        "standalone deny must not arm a recheck window");
    assert_eq!(s.core.failed_attempts(), 1);
    assert_eq!(s.core.backoff_until(), s.now_ms + 2_000);

    // Second card during the backoff window is silently dropped.
    s.tick(500);
    let eff2 = s.card(1, 2);
    assert!(eff2.is_empty());

    // After the backoff expires, another miss escalates to 4s.
    s.tick(2_000);
    let eff3 = s.card(1, 2);
    assert!(contains_outcome(&eff3, Outcome::Denied));
    assert_eq!(s.core.failed_attempts(), 2);
    assert_eq!(s.core.backoff_until(), s.now_ms + 4_000);
}

#[test]
fn standalone_sync_complete_is_a_noop_even_with_no_pending() {
    // Defensive: sync_task is gated off in standalone, but if a stray
    // SyncComplete sneaks in, it must do nothing harmful.
    let mut s = Sim::new_standalone();
    let eff = s.sync();
    assert!(eff.is_empty());
}

// ---------------------------------------------------------------------------
// Property tests (A1, A2, A3, A4, A5 together)
// ---------------------------------------------------------------------------

/// One step of a randomly-generated trace.
#[derive(Clone, Debug)]
enum Step {
    Card { fob: u32, nfc: u32, dt_ms: u32 },
    Sync { dt_ms: u32 },
    Watchdog { dt_ms: u32 },
    AddFob { fob: u32 },
    RemoveFob { fob: u32 },
}

fn arb_step() -> impl Strategy<Value = Step> {
    // Constrain credential space to a small set so cache hits happen often
    // enough to exercise grant paths.
    let cred = 1u32..50;
    prop_oneof![
        (cred.clone(), cred.clone(), 0u32..15_000)
            .prop_map(|(fob, nfc, dt_ms)| Step::Card { fob, nfc, dt_ms }),
        (0u32..15_000).prop_map(|dt_ms| Step::Sync { dt_ms }),
        (0u32..15_000).prop_map(|dt_ms| Step::Watchdog { dt_ms }),
        cred.clone().prop_map(|fob| Step::AddFob { fob }),
        cred.prop_map(|fob| Step::RemoveFob { fob }),
    ]
}

fn arb_trace() -> impl Strategy<Value = Vec<Step>> {
    proptest::collection::vec(arb_step(), 1..80)
}

proptest! {
    #![proptest_config(ProptestConfig {
        cases: 1024,
        rng_algorithm: proptest::test_runner::RngAlgorithm::ChaCha,
        ..ProptestConfig::default()
    })]

    /// HEADLINE INVARIANT (A1, A2, A3 combined):
    /// every `OpenDoor` effect coincides with a fob-cache membership of the
    /// credential being decided at that moment.
    ///
    /// - For `Card` inputs: either fob or nfc is in `fobs` at decision time.
    /// - For `SyncComplete` inputs: the pending fob or nfc is in the
    ///   *current* `fobs` (which is what `step()` is told). This proves
    ///   the sync path cannot grant for a credential the cache doesn't
    ///   actually contain right now.
    #[test]
    fn prop_no_grant_without_current_cache_hit(trace in arb_trace()) {
        let mut s = Sim::new();
        for step in trace {
            match step {
                Step::AddFob { fob } => s.add_fob(fob),
                Step::RemoveFob { fob } => s.remove_fob(fob),
                Step::Card { fob, nfc, dt_ms } => {
                    s.tick(dt_ms as u64);
                    let pre_fobs = s.fobs.clone();
                    let eff = s.card(fob, nfc);
                    if contains_open_door(&eff) {
                        prop_assert!(pre_fobs.contains(&fob) || pre_fobs.contains(&nfc),
                            "grant emitted but neither fob {} nor nfc {} was in cache {:?}",
                            fob, nfc, pre_fobs);
                    }
                }
                Step::Sync { dt_ms } => {
                    s.tick(dt_ms as u64);
                    // Snapshot the pending recheck *before* sync consumes it.
                    let pending = s.core.pending_recheck();
                    let pre_fobs = s.fobs.clone();
                    let eff = s.sync();
                    if contains_open_door(&eff) {
                        let (pfob, pnfc, _) = pending
                            .expect("grant from sync requires a pending recheck");
                        prop_assert!(pre_fobs.contains(&pfob) || pre_fobs.contains(&pnfc),
                            "sync-grant for fob={} nfc={} but cache is {:?}",
                            pfob, pnfc, pre_fobs);
                    }
                }
                Step::Watchdog { dt_ms } => {
                    s.tick(dt_ms as u64);
                    let eff = s.input(Input::WatchdogFeed);
                    // Watchdog input never grants.
                    prop_assert!(!contains_open_door(&eff));
                }
            }
        }
    }

    /// A4: while `now < backoff_until`, no `Card` input may produce *any*
    /// effects (it is silently dropped).
    #[test]
    fn prop_backoff_window_is_silent(trace in arb_trace()) {
        let mut s = Sim::new();
        for step in trace {
            match step {
                Step::AddFob { fob } => s.add_fob(fob),
                Step::RemoveFob { fob } => s.remove_fob(fob),
                Step::Card { fob, nfc, dt_ms } => {
                    s.tick(dt_ms as u64);
                    let in_backoff = s.now_ms < s.core.backoff_until();
                    let eff = s.card(fob, nfc);
                    if in_backoff {
                        prop_assert!(eff.is_empty(),
                            "card during backoff produced effects: {:?}", eff);
                    }
                }
                Step::Sync { dt_ms } => { s.tick(dt_ms as u64); let _ = s.sync(); }
                Step::Watchdog { dt_ms } => { s.tick(dt_ms as u64); let _ = s.input(Input::WatchdogFeed); }
            }
        }
    }

    /// A5: a `SyncComplete` arriving after the 10s recheck window must
    /// never emit `OpenDoor`. (And specifically must never emit the
    /// post-deadline "denied" feedback either — the expired-pending branch
    /// silently returns no effects.)
    #[test]
    fn prop_expired_recheck_never_grants(trace in arb_trace()) {
        let mut s = Sim::new();
        for step in trace {
            match step {
                Step::AddFob { fob } => s.add_fob(fob),
                Step::RemoveFob { fob } => s.remove_fob(fob),
                Step::Card { fob, nfc, dt_ms } => {
                    s.tick(dt_ms as u64);
                    let _ = s.card(fob, nfc);
                }
                Step::Sync { dt_ms } => {
                    s.tick(dt_ms as u64);
                    // Snapshot deadline before sync consumes pending.
                    let pending = s.core.pending_recheck();
                    let expired = pending.map(|(_, _, d)| s.now_ms > d).unwrap_or(false);
                    let eff = s.sync();
                    if expired {
                        prop_assert!(!contains_open_door(&eff),
                            "A5: expired recheck must not grant");
                        prop_assert!(eff.is_empty(),
                            "A5: expired recheck branch must emit no effects, got {:?}", eff);
                    }
                }
                Step::Watchdog { dt_ms } => { s.tick(dt_ms as u64); let _ = s.input(Input::WatchdogFeed); }
            }
        }
    }

    /// Audit-trail invariant: every `OpenDoor` from a `Card` input is
    /// accompanied (in the same step) by a `Record { allowed: true }`.
    /// Sync-path grants do NOT emit a Record (matches main.rs), so we only
    /// check Card-driven grants.
    #[test]
    fn prop_card_grant_implies_record_allowed_true(trace in arb_trace()) {
        let mut s = Sim::new();
        for step in trace {
            match step {
                Step::AddFob { fob } => s.add_fob(fob),
                Step::RemoveFob { fob } => s.remove_fob(fob),
                Step::Card { fob, nfc, dt_ms } => {
                    s.tick(dt_ms as u64);
                    let eff = s.card(fob, nfc);
                    if contains_open_door(&eff) {
                        prop_assert!(eff.iter().any(|e| matches!(
                            e,
                            Effect::Record(AccessEvent { allowed: true, .. })
                        )), "card grant without allowed=true record: {:?}", eff);
                    }
                }
                Step::Sync { dt_ms } => { s.tick(dt_ms as u64); let _ = s.sync(); }
                Step::Watchdog { dt_ms } => { s.tick(dt_ms as u64); let _ = s.input(Input::WatchdogFeed); }
            }
        }
    }

    /// Every processed (non-backoff-dropped) `Card` input produces exactly
    /// one `Feedback(...)` effect.
    #[test]
    fn prop_processed_card_emits_exactly_one_feedback(trace in arb_trace()) {
        let mut s = Sim::new();
        for step in trace {
            match step {
                Step::AddFob { fob } => s.add_fob(fob),
                Step::RemoveFob { fob } => s.remove_fob(fob),
                Step::Card { fob, nfc, dt_ms } => {
                    s.tick(dt_ms as u64);
                    let in_backoff = s.now_ms < s.core.backoff_until();
                    let eff = s.card(fob, nfc);
                    if !in_backoff {
                        let feedbacks = eff.iter().filter(|e| matches!(e, Effect::Feedback(_))).count();
                        prop_assert_eq!(feedbacks, 1,
                            "expected exactly 1 feedback effect, got {} in {:?}",
                            feedbacks, eff);
                    }
                }
                Step::Sync { dt_ms } => { s.tick(dt_ms as u64); let _ = s.sync(); }
                Step::Watchdog { dt_ms } => { s.tick(dt_ms as u64); let _ = s.input(Input::WatchdogFeed); }
            }
        }
    }
}

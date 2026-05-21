//! Pure library surface of the access-controller crate.
//!
//! Only modules with zero hardware/HAL dependencies live here. They are
//! shared between the firmware binary (`src/main.rs`) and host-side
//! deterministic simulation tests (`tests/*.rs`).
//!
//! Build modes:
//! - Default (firmware): `cargo build` with `--target xtensa-esp32-none-elf`,
//!   feature `esp32` (enabled by default). `no_std`.
//! - Simulation/tests: `cargo test --no-default-features --features sim` on
//!   the host; uses `std` so we can run proptest and standard `#[test]`s.

#![cfg_attr(not(feature = "sim"), no_std)]

extern crate alloc;

pub mod core;
pub mod crypto;
pub mod decode;
pub mod events;

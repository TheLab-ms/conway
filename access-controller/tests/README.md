# Access Controller Tests

This directory contains unit tests for the access controller firmware's pure logic functions.

## Running Tests

Since the main crate targets ESP32 (`#![no_std]`), these tests are in a separate crate that runs on the host system. You must explicitly specify a host target:

```bash
# From this directory
cargo +stable test --target aarch64-apple-darwin  # macOS ARM
cargo +stable test --target x86_64-apple-darwin   # macOS Intel
cargo +stable test --target x86_64-unknown-linux-gnu  # Linux
```

Or set an override for this directory:
```bash
rustup override set stable
cargo test
```

//! Build script for compile-time configuration injection.
//!
//! Set environment variables before building to configure the firmware:
//!
//!   CONWAY_SSID=MyWiFi \
//!   CONWAY_PASSWORD=secret123 \
//!   CONWAY_HOST=192.168.1.68 \
//!   CONWAY_PORT=8080 \
//!   CONWAY_UNLOCK_SECRET=mysecret \
//!   cargo build --release
//!
//! Or use the build.sh wrapper script.

fn main() {
    // Re-run build script if these environment variables change
    println!("cargo::rerun-if-env-changed=CONWAY_SSID");
    println!("cargo::rerun-if-env-changed=CONWAY_PASSWORD");
    println!("cargo::rerun-if-env-changed=CONWAY_HOST");
    println!("cargo::rerun-if-env-changed=CONWAY_PORT");
    println!("cargo::rerun-if-env-changed=CONWAY_UNLOCK_SECRET");
}

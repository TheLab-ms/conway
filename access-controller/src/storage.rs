//! Configuration for Conway Access Controller.
//!
//! Network configuration is embedded at compile time via environment variables.
//! Fob cache is stored in memory only (no flash persistence).

pub const MAX_FOBS: usize = 512;

/// Conway server configuration, embedded at compile time.
#[derive(Clone)]
pub struct Config {
    pub ssid: &'static str,
    pub password: &'static str,
    pub conway_host: &'static str,
    pub conway_port: u16,
}

impl Config {
    pub fn get() -> Self {
        Self {
            ssid: option_env!("CONWAY_SSID").unwrap_or("unconfigured"),
            password: option_env!("CONWAY_PASSWORD").unwrap_or(""),
            conway_host: option_env!("CONWAY_HOST").unwrap_or("192.168.1.1"),
            conway_port: match option_env!("CONWAY_PORT") {
                Some(s) => parse_port(s),
                None => 8080,
            },
        }
    }
}

const fn parse_port(s: &str) -> u16 {
    let bytes = s.as_bytes();
    let mut result: u16 = 0;
    let mut i = 0;
    while i < bytes.len() {
        let digit = bytes[i];
        if digit >= b'0' && digit <= b'9' {
            result = result * 10 + (digit - b'0') as u16;
        }
        i += 1;
    }
    if result == 0 { 8080 } else { result }
}

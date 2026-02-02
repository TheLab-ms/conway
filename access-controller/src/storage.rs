pub const MAX_FOBS: usize = 512;

pub const SSID: &str = match option_env!("CONWAY_SSID") {
    Some(s) => s,
    None => "unconfigured",
};
pub const PASSWORD: &str = match option_env!("CONWAY_PASSWORD") {
    Some(s) => s,
    None => "",
};
pub const CONWAY_HOST: &str = match option_env!("CONWAY_HOST") {
    Some(s) => s,
    None => "192.168.1.1",
};
pub const CONWAY_PORT: u16 = parse_port(option_env!("CONWAY_PORT"));

const fn parse_port(s: Option<&str>) -> u16 {
    match s {
        Some(s) => {
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
        None => 8080,
    }
}

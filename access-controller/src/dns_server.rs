//! Captive-portal DNS responder.
//!
//! Listens on UDP/53 and answers every A-record query with the AP's IP
//! (192.168.4.1). This is what makes phones automatically pop the
//! "Sign in to network" sheet pointing at our config UI: their captive
//! portal probes (e.g. clients3.google.com/generate_204) resolve to us,
//! our HTTP server returns a 302 to /config, and the OS treats that as
//! "captive portal detected, prompt the user".
//!
//! Question parsing is intentionally minimal - we accept any QTYPE and
//! ignore QCLASS - but the answer section is a strict A/IN/TTL/4 record
//! using a compressed name pointer back to the question, which all
//! standard resolvers handle.

use embassy_net::udp::{PacketMetadata, UdpSocket};
use embassy_net::{IpListenEndpoint, Stack};

use crate::dhcp_server::AP_IP;

const DNS_PORT: u16 = 53;
const TTL_SECS: u32 = 60;

#[embassy_executor::task]
pub async fn dns_server_task(stack: &'static Stack<'static>) {
    loop {
        if stack.config_v4().is_some() {
            break;
        }
        embassy_time::Timer::after(embassy_time::Duration::from_millis(200)).await;
    }
    log::info!("dns: captive portal responder starting");

    let mut rx_meta = [PacketMetadata::EMPTY; 4];
    let mut tx_meta = [PacketMetadata::EMPTY; 4];
    let mut rx_buf = [0u8; 1024];
    let mut tx_buf = [0u8; 1024];

    let mut socket = UdpSocket::new(
        *stack,
        &mut rx_meta,
        &mut rx_buf,
        &mut tx_meta,
        &mut tx_buf,
    );

    if let Err(e) = socket.bind(IpListenEndpoint {
        addr: None,
        port: DNS_PORT,
    }) {
        log::error!("dns: bind failed: {:?}", e);
        return;
    }

    let mut buf = [0u8; 512];
    let mut out = [0u8; 512];

    loop {
        let (n, meta) = match socket.recv_from(&mut buf).await {
            Ok(v) => v,
            Err(e) => {
                log::warn!("dns: recv error: {:?}", e);
                continue;
            }
        };
        if n < 12 {
            continue;
        }

        // Parse header.
        let qd = u16::from_be_bytes([buf[4], buf[5]]);
        if qd == 0 {
            continue;
        }

        // Locate end of the (first) question name: sequence of length-
        // prefixed labels terminated by a 0 byte.
        let mut p = 12usize;
        while p < n && buf[p] != 0 {
            let l = buf[p] as usize;
            if l & 0xC0 != 0 {
                // pointer in a question - shouldn't happen but bail.
                p = n;
                break;
            }
            p += 1 + l;
            if p >= n {
                break;
            }
        }
        if p + 1 + 4 > n {
            continue; // truncated
        }
        let qname_end = p + 1; // past the 0 terminator
        let qtype_end = qname_end + 4; // QTYPE(2) + QCLASS(2)
        let question_len = qtype_end - 12;

        // Build response. We answer only the first question and set the
        // truncation bit on neither - clients with multiple questions
        // typically retransmit, which is fine for our use case.
        let resp_len = 12 + question_len + 16;
        if resp_len > out.len() {
            continue;
        }

        // Header.
        out[0..2].copy_from_slice(&buf[0..2]); // ID
        out[2] = 0x84; // QR=1, Opcode=0, AA=1, TC=0, RD=copied below
        out[2] |= buf[2] & 0x01; // preserve RD
        out[3] = 0x80; // RA=1, RCODE=0
        out[4..6].copy_from_slice(&[0, 1]); // QDCOUNT
        out[6..8].copy_from_slice(&[0, 1]); // ANCOUNT
        out[8..12].copy_from_slice(&[0, 0, 0, 0]); // NS, AR

        // Copy question verbatim.
        out[12..12 + question_len].copy_from_slice(&buf[12..12 + question_len]);

        // Answer section.
        let a = 12 + question_len;
        // NAME: pointer to offset 12 (the question name).
        out[a] = 0xC0;
        out[a + 1] = 0x0C;
        // TYPE = A (1)
        out[a + 2] = 0;
        out[a + 3] = 1;
        // CLASS = IN (1)
        out[a + 4] = 0;
        out[a + 5] = 1;
        // TTL
        out[a + 6..a + 10].copy_from_slice(&TTL_SECS.to_be_bytes());
        // RDLENGTH = 4
        out[a + 10] = 0;
        out[a + 11] = 4;
        // RDATA = AP IP
        out[a + 12..a + 16].copy_from_slice(&AP_IP);

        if let Err(e) = socket.send_to(&out[..resp_len], meta.endpoint).await {
            log::warn!("dns: send error: {:?}", e);
        }
    }
}

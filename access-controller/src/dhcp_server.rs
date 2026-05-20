//! Minimal DHCPv4 server for AP onboarding mode.
//!
//! Hands out a single IPv4 lease from a fixed pool to whichever client
//! probes us first. Just enough RFC-2131 to keep iOS/Android/Windows
//! happy when they associate with the onboarding SSID:
//!
//! - DISCOVER → OFFER (ip = pool[round_robin])
//! - REQUEST  → ACK   (or NAK if requested address is wrong)
//! - DECLINE / RELEASE / INFORM are logged and ignored.
//!
//! We intentionally do not track leases beyond the latest client; the
//! AP is online for a few minutes during initial setup and the next
//! client just gets the next slot in the pool.

use embassy_net::udp::{PacketMetadata, UdpSocket};
use embassy_net::{IpEndpoint, IpListenEndpoint, Stack};

/// AP-side static IP, also the DHCP server identifier + gateway + DNS.
pub const AP_IP: [u8; 4] = [192, 168, 4, 1];
const NETMASK: [u8; 4] = [255, 255, 255, 0];
const LEASE_SECS: u32 = 3600;

const SERVER_PORT: u16 = 67;
const CLIENT_PORT: u16 = 68;

/// Pool of addresses we will hand out (round-robin).
const POOL: [[u8; 4]; 8] = [
    [192, 168, 4, 100],
    [192, 168, 4, 101],
    [192, 168, 4, 102],
    [192, 168, 4, 103],
    [192, 168, 4, 104],
    [192, 168, 4, 105],
    [192, 168, 4, 106],
    [192, 168, 4, 107],
];

// DHCP magic cookie.
const MAGIC: [u8; 4] = [0x63, 0x82, 0x53, 0x63];

// DHCP message types (option 53 value).
const DHCP_DISCOVER: u8 = 1;
const DHCP_OFFER: u8 = 2;
const DHCP_REQUEST: u8 = 3;
const DHCP_DECLINE: u8 = 4;
const DHCP_ACK: u8 = 5;
const DHCP_NAK: u8 = 6;

#[embassy_executor::task]
pub async fn dhcp_server_task(stack: &'static Stack<'static>) {
    // Wait for the stack to come up with our static IP.
    loop {
        if stack.config_v4().is_some() {
            break;
        }
        embassy_time::Timer::after(embassy_time::Duration::from_millis(200)).await;
    }
    log::info!("dhcp: server starting on {:?}", AP_IP);

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
        port: SERVER_PORT,
    }) {
        log::error!("dhcp: bind failed: {:?}", e);
        return;
    }

    let mut next_idx: usize = 0;
    let mut buf = [0u8; 1024];

    loop {
        let (n, _meta) = match socket.recv_from(&mut buf).await {
            Ok(v) => v,
            Err(e) => {
                log::warn!("dhcp: recv error: {:?}", e);
                continue;
            }
        };
        if n < 240 {
            continue;
        }
        let pkt = &buf[..n];

        // RFC 2131 BOOTP layout: op(1)=1 for request.
        if pkt[0] != 1 {
            continue;
        }
        let xid = [pkt[4], pkt[5], pkt[6], pkt[7]];
        let chaddr: [u8; 16] = pkt[28..44].try_into().unwrap();

        // Validate magic cookie.
        if pkt[236..240] != MAGIC {
            continue;
        }

        // Parse options to find the message type and the requested IP.
        let opts = &pkt[240..];
        let mut msg_type: u8 = 0;
        let mut requested_ip: Option<[u8; 4]> = None;
        let mut server_id: Option<[u8; 4]> = None;
        let mut i = 0;
        while i < opts.len() {
            let code = opts[i];
            if code == 0xFF {
                break;
            }
            if code == 0 {
                i += 1;
                continue;
            }
            if i + 1 >= opts.len() {
                break;
            }
            let len = opts[i + 1] as usize;
            let val_start = i + 2;
            let val_end = val_start + len;
            if val_end > opts.len() {
                break;
            }
            let val = &opts[val_start..val_end];
            match code {
                53 if len == 1 => msg_type = val[0],
                50 if len == 4 => requested_ip = Some([val[0], val[1], val[2], val[3]]),
                54 if len == 4 => server_id = Some([val[0], val[1], val[2], val[3]]),
                _ => {}
            }
            i = val_end;
        }

        match msg_type {
            DHCP_DISCOVER => {
                let offer = POOL[next_idx % POOL.len()];
                next_idx = next_idx.wrapping_add(1);
                log::info!("dhcp: DISCOVER -> OFFER {:?}", offer);
                let reply = build_reply(xid, chaddr, offer, DHCP_OFFER);
                send_broadcast(&mut socket, &reply).await;
            }
            DHCP_REQUEST => {
                let want = requested_ip.unwrap_or_else(|| {
                    // If no Requested-IP option, fall back to ciaddr.
                    [pkt[12], pkt[13], pkt[14], pkt[15]]
                });
                let in_pool = POOL.iter().any(|p| *p == want);
                let bad_server = matches!(server_id, Some(sid) if sid != AP_IP);
                if !in_pool || bad_server {
                    log::info!("dhcp: REQUEST {:?} -> NAK", want);
                    let reply = build_reply(xid, chaddr, [0, 0, 0, 0], DHCP_NAK);
                    send_broadcast(&mut socket, &reply).await;
                } else {
                    log::info!("dhcp: REQUEST {:?} -> ACK", want);
                    let reply = build_reply(xid, chaddr, want, DHCP_ACK);
                    send_broadcast(&mut socket, &reply).await;
                }
            }
            DHCP_DECLINE => {
                log::warn!("dhcp: DECLINE from client");
            }
            other => {
                log::debug!("dhcp: ignoring message type {}", other);
            }
        }
    }
}

async fn send_broadcast(socket: &mut UdpSocket<'_>, pkt: &[u8]) {
    let dst = IpEndpoint::new(
        smoltcp::wire::IpAddress::Ipv4(smoltcp::wire::Ipv4Address::new(255, 255, 255, 255)),
        CLIENT_PORT,
    );
    if let Err(e) = socket.send_to(pkt, dst).await {
        log::warn!("dhcp: send error: {:?}", e);
    }
}

/// Build a minimal BOOTP/DHCP reply (300 bytes, no padding beyond the
/// option block).
fn build_reply(xid: [u8; 4], chaddr: [u8; 16], yiaddr: [u8; 4], msg_type: u8) -> [u8; 300] {
    let mut p = [0u8; 300];

    p[0] = 2; // BOOTREPLY
    p[1] = 1; // ethernet
    p[2] = 6; // hw addr len
    p[3] = 0; // hops
    p[4..8].copy_from_slice(&xid);
    // secs(8..10), flags(10..12) = 0
    // ciaddr(12..16) = 0
    p[16..20].copy_from_slice(&yiaddr); // yiaddr
    // siaddr(20..24) = AP itself (helpful for some clients)
    p[20..24].copy_from_slice(&AP_IP);
    // giaddr(24..28) = 0
    p[28..44].copy_from_slice(&chaddr);
    // sname(44..108), file(108..236) = 0
    p[236..240].copy_from_slice(&MAGIC);

    // Options
    let mut i = 240;
    // 53: message type
    p[i] = 53;
    p[i + 1] = 1;
    p[i + 2] = msg_type;
    i += 3;
    // 54: server identifier
    p[i] = 54;
    p[i + 1] = 4;
    p[i + 2..i + 6].copy_from_slice(&AP_IP);
    i += 6;
    if msg_type == DHCP_OFFER || msg_type == DHCP_ACK {
        // 51: lease time
        p[i] = 51;
        p[i + 1] = 4;
        p[i + 2..i + 6].copy_from_slice(&LEASE_SECS.to_be_bytes());
        i += 6;
        // 1: subnet mask
        p[i] = 1;
        p[i + 1] = 4;
        p[i + 2..i + 6].copy_from_slice(&NETMASK);
        i += 6;
        // 3: router
        p[i] = 3;
        p[i + 1] = 4;
        p[i + 2..i + 6].copy_from_slice(&AP_IP);
        i += 6;
        // 6: DNS server (us)
        p[i] = 6;
        p[i + 1] = 4;
        p[i + 2..i + 6].copy_from_slice(&AP_IP);
        i += 6;
    }
    // 255: end
    p[i] = 0xFF;

    p
}

//! Minimal HTTP server.
//!
//! Single-connection accept loop bound to TCP/80. Serves a small HTML status
//! page at `GET /` and `GET /status`, accepts firmware uploads at
//! `POST /ota`, and can flip back to the previous slot via
//! `POST /ota/rollback`. Everything else returns 404 / 405.
//!
//! Intentionally minimal: no keep-alive, no auth, no TLS, no concurrent
//! connections. OTA is gated only by being on the same LAN.

use core::fmt::Write as FmtWrite;
use embassy_net::tcp::TcpSocket;
use embassy_net::Stack;
use embassy_sync::blocking_mutex::raw::CriticalSectionRawMutex;
use embassy_sync::mutex::Mutex;
use embassy_time::{Duration, Instant, Timer};
use embedded_io_async::Write;
use heapless::String as HString;

use crate::fob_store::{self, LocalFob, MAX_LABEL_LEN, MAX_LOCAL_FOBS};
use crate::ota::{self, OtaError, OtaWriter};
use crate::settings::{self, Settings, MAX_PASSWORD, MAX_SSID};
use crate::{
    DeviceMode, LastSwipe, PendingConfig, RuntimeConfig, EVENT_BUFFER, MANUAL_UNLOCK, MAX_FOBS,
    PENDING_CONFIG, PENDING_CONFIG_TTL, WATCHDOG_FEED,
};
use access_controller::signing;

const HTTP_PORT: u16 = 80;
/// Timeout for normal short requests.
const IO_TIMEOUT: Duration = Duration::from_secs(5);
/// Timeout used while streaming an OTA payload - flash erase/write is
/// slow and a full image can take ~30 s on a busy LAN.
const OTA_IO_TIMEOUT: Duration = Duration::from_secs(60);
/// Header read buffer. Must be large enough for the request line plus
/// any headers we care about (Content-Length).
const REQ_BUF_LEN: usize = 2048;
/// Per-read body chunk size. Sized to be a multiple of the flash sector
/// (4 KiB) so we keep flash writes well batched, while still small
/// enough to leave plenty of TCP rx headroom.
const OTA_CHUNK: usize = 2048;

/// Max size for a `POST /config` form body. The on-wire form is
/// well under 256 bytes even at max field lengths.
const CONFIG_BODY_MAX: usize = 512;

/// HTTP server task. Runs forever, accepting one connection at a time.
#[embassy_executor::task]
pub async fn http_server_task(
    stack: &'static Stack<'static>,
    fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    local_fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
    etag: &'static Mutex<CriticalSectionRawMutex, HString<64>>,
    last_swipe: &'static Mutex<CriticalSectionRawMutex, Option<LastSwipe>>,
    rt: &'static RuntimeConfig,
) {
    // Wait for the network stack to be ready before binding.
    loop {
        if stack.is_link_up() && stack.config_v4().is_some() {
            break;
        }
        embassy_time::Timer::after(Duration::from_millis(200)).await;
    }
    log::info!("http: network ready, listening on :{}", HTTP_PORT);

    // Socket buffers live on the task stack and are reused for every
    // connection. 4 KiB rx gives the TCP window enough headroom to
    // sustain decent throughput during OTA uploads.
    let mut rx_buf = [0u8; 4096];
    let mut tx_buf = [0u8; 2048];

    loop {
        let mut socket = TcpSocket::new(*stack, &mut rx_buf, &mut tx_buf);
        socket.set_timeout(Some(IO_TIMEOUT));

        log::debug!("http: waiting for connection");
        if let Err(e) = socket.accept(HTTP_PORT).await {
            log::warn!("http: accept failed: {:?}", e);
            embassy_time::Timer::after(Duration::from_millis(100)).await;
            continue;
        }

        let peer = socket.remote_endpoint();
        log::info!("http: connection from {:?}", peer);

        handle_connection(&mut socket, fobs, local_fobs, etag, last_swipe, stack, rt).await;

        let _ = socket.flush().await;
        socket.close();
    }
}

async fn handle_connection(
    socket: &mut TcpSocket<'_>,
    fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    local_fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
    etag: &Mutex<CriticalSectionRawMutex, HString<64>>,
    last_swipe: &Mutex<CriticalSectionRawMutex, Option<LastSwipe>>,
    stack: &Stack<'static>,
    rt: &'static RuntimeConfig,
) {
    // Read until we have the request headers (terminated by \r\n\r\n).
    let mut buf = [0u8; REQ_BUF_LEN];
    let mut len = 0usize;
    let header_end = loop {
        if len == buf.len() {
            log::warn!("http: request headers exceed {} bytes, dropping", REQ_BUF_LEN);
            send_status_line(socket, "431 Request Header Fields Too Large", b"too large\n").await;
            return;
        }
        match socket.read(&mut buf[len..]).await {
            Ok(0) => {
                log::debug!("http: peer closed before request complete");
                return;
            }
            Ok(n) => {
                len += n;
                if let Some(pos) = find_double_crlf(&buf[..len]) {
                    break pos;
                }
            }
            Err(e) => {
                log::warn!("http: read error: {:?}", e);
                return;
            }
        }
    };

    // Parse the request line: METHOD SP TARGET SP HTTP-VERSION CRLF
    let headers_str = match core::str::from_utf8(&buf[..header_end]) {
        Ok(s) => s,
        Err(_) => {
            send_status_line(socket, "400 Bad Request", b"invalid utf-8\n").await;
            return;
        }
    };
    let request_line = headers_str.lines().next().unwrap_or("");

    let mut parts = request_line.split(' ');
    let method = parts.next().unwrap_or("");
    let target = parts.next().unwrap_or("");

    log::info!("http: {} {}", method, target);

    let path = target.split('?').next().unwrap_or("");

    // Body bytes already read past the header terminator.
    let leftover = &buf[header_end..len];

    match (method, path) {
        // In onboarding mode, redirect "/" to the config page so that
        // captive-portal browsers land right on the form.
        ("GET", "/") if rt.mode == DeviceMode::Onboarding => {
            send_redirect(socket, "/config").await;
        }
        ("GET", "/") | ("GET", "/status") => {
            send_status_page(socket, fobs, local_fobs, etag, last_swipe, stack, rt).await;
        }
        ("GET", "/config") => {
            send_config_page(socket, rt).await;
        }
        ("POST", "/config") => {
            let cl = match parse_content_length(headers_str) {
                Some(n) if (n as usize) <= CONFIG_BODY_MAX => n,
                Some(_) => {
                    send_status_line(socket, "413 Payload Too Large", b"body too large\n").await;
                    return;
                }
                None => {
                    send_status_line(socket, "411 Length Required", b"need Content-Length\n").await;
                    return;
                }
            };
            handle_config_post(socket, cl, leftover, rt).await;
        }
        ("GET", "/fobs") => {
            send_fobs_page(socket, local_fobs, rt).await;
        }
        ("GET", "/swipes") => {
            send_swipes_page(socket).await;
        }
        ("POST", "/fobs") => {
            let cl = match parse_content_length(headers_str) {
                Some(n) if (n as usize) <= CONFIG_BODY_MAX => n,
                Some(_) => {
                    send_status_line(socket, "413 Payload Too Large", b"body too large\n").await;
                    return;
                }
                None => {
                    send_status_line(socket, "411 Length Required", b"need Content-Length\n").await;
                    return;
                }
            };
            handle_fob_add(socket, cl, leftover, local_fobs).await;
        }
        ("POST", "/fobs/delete") => {
            let cl = match parse_content_length(headers_str) {
                Some(n) if (n as usize) <= CONFIG_BODY_MAX => n,
                Some(_) => {
                    send_status_line(socket, "413 Payload Too Large", b"body too large\n").await;
                    return;
                }
                None => {
                    send_status_line(socket, "411 Length Required", b"need Content-Length\n").await;
                    return;
                }
            };
            handle_fob_delete(socket, cl, leftover, local_fobs).await;
        }
        // Captive portal probes - send everyone to /config.
        ("GET", "/generate_204")
        | ("GET", "/gen_204")
        | ("GET", "/hotspot-detect.html")
        | ("GET", "/library/test/success.html")
        | ("GET", "/connecttest.txt")
        | ("GET", "/ncsi.txt")
        | ("GET", "/redirect")
        | ("GET", "/success.txt") => {
            send_redirect(socket, "/config").await;
        }
        ("POST", "/ota") => {
            let cl = match parse_content_length(headers_str) {
                Some(n) => n,
                None => {
                    send_status_line(socket, "411 Length Required", b"need Content-Length\n").await;
                    return;
                }
            };
            handle_ota_upload(socket, cl, leftover).await;
        }
        ("POST", "/ota/rollback") => {
            handle_ota_rollback(socket).await;
        }
        ("POST", "/unlock") => {
            handle_manual_unlock(socket, rt).await;
        }
        ("GET", _) if rt.mode == DeviceMode::Onboarding => {
            // Any unknown GET while onboarding: bounce to /config so
            // OS captive-portal heuristics fire.
            send_redirect(socket, "/config").await;
        }
        ("GET", _) => {
            send_status_line(socket, "404 Not Found", b"not found\n").await;
        }
        _ => {
            send_status_line(socket, "405 Method Not Allowed", b"method not allowed\n").await;
        }
    }
}

/// Stream an OTA image into the inactive slot, flip otadata, then
/// reset the device. Sends a plaintext result to the client first so
/// the user sees success (or the specific error) before the link drops.
async fn handle_ota_upload(socket: &mut TcpSocket<'_>, content_length: u32, leftover: &[u8]) {
    // Use a longer timeout while we are blocked on flash writes.
    socket.set_timeout(Some(OTA_IO_TIMEOUT));

    let mut writer = match OtaWriter::begin(content_length) {
        Ok(w) => w,
        Err(e) => {
            log::warn!("ota: begin failed: {}", e);
            send_ota_error(socket, e).await;
            return;
        }
    };
    log::info!(
        "ota: upload starting -> slot={:?} size={}",
        writer.target_slot(),
        content_length
    );

    // Consume any body bytes that arrived in the header buffer first.
    if !leftover.is_empty() {
        if let Err(e) = writer.write(leftover) {
            log::warn!("ota: first write failed: {}", e);
            send_ota_error(socket, e).await;
            return;
        }
    }

    let mut chunk = [0u8; OTA_CHUNK];
    let mut last_log_pct: u8 = 0;

    while writer.bytes_accepted() < writer.expected() {
        let want =
            (writer.expected() - writer.bytes_accepted()).min(OTA_CHUNK as u32) as usize;
        match socket.read(&mut chunk[..want]).await {
            Ok(0) => {
                log::warn!("ota: peer closed mid-upload");
                send_ota_error(socket, OtaError::SizeMismatch).await;
                return;
            }
            Ok(n) => {
                if let Err(e) = writer.write(&chunk[..n]) {
                    log::warn!("ota: write failed: {}", e);
                    send_ota_error(socket, e).await;
                    return;
                }
                // Feed the watchdog (indirectly, via access_task) and
                // yield so other tasks get a turn between sector erases.
                WATCHDOG_FEED.signal(());
                embassy_futures::yield_now().await;

                let pct = ((writer.bytes_accepted() as u64 * 100)
                    / writer.expected() as u64) as u8;
                if pct >= last_log_pct.saturating_add(10) {
                    log::info!(
                        "ota: progress {}% ({}/{})",
                        pct,
                        writer.bytes_accepted(),
                        writer.expected()
                    );
                    last_log_pct = pct;
                }
            }
            Err(e) => {
                log::warn!("ota: read error: {:?}", e);
                send_ota_error(socket, OtaError::SizeMismatch).await;
                return;
            }
        }
    }

    let new_slot = match writer.finish() {
        Ok(s) => s,
        Err(e) => {
            log::error!("ota: finish failed: {}", e);
            send_ota_error(socket, e).await;
            return;
        }
    };

    // Send success and reboot. We deliberately flush before resetting
    // so the client sees the response.
    let mut body: HString<128> = HString::new();
    let _ = write!(
        body,
        "ok: activated {} ({} bytes), rebooting\n",
        ota::slot_label(new_slot),
        content_length
    );
    send_text(socket, "200 OK", body.as_bytes()).await;
    let _ = socket.flush().await;
    socket.close();

    log::warn!("ota: rebooting into new slot");
    Timer::after(Duration::from_millis(250)).await;
    esp_hal::system::software_reset();
}

/// Flip otadata back to the other slot and reboot.
async fn handle_ota_rollback(socket: &mut TcpSocket<'_>) {
    match ota::rollback() {
        Ok(slot) => {
            let mut body: HString<96> = HString::new();
            let _ = write!(body, "ok: rolled back to {}, rebooting\n", ota::slot_label(slot));
            send_text(socket, "200 OK", body.as_bytes()).await;
            let _ = socket.flush().await;
            socket.close();
            log::warn!("ota: rollback -> {:?}, rebooting", slot);
            Timer::after(Duration::from_millis(250)).await;
            esp_hal::system::software_reset();
        }
        Err(e) => {
            log::warn!("ota: rollback failed: {}", e);
            send_ota_error(socket, e).await;
        }
    }
}

async fn send_ota_error(socket: &mut TcpSocket<'_>, err: OtaError) {
    let mut body: HString<96> = HString::new();
    let _ = write!(body, "ota error: {}\n", err);
    send_text(socket, err.http_status(), body.as_bytes()).await;
}

/// Operator-initiated door pulse. Forbidden while onboarding (the
/// device isn't yet trusted to be on a private LAN). Otherwise the
/// access_task observes `MANUAL_UNLOCK`, fires `DOOR_SIGNAL` +
/// `READER_FEEDBACK::Granted`, and records an audit entry with the
/// `MANUAL_UNLOCK_FOB` sentinel.
async fn handle_manual_unlock(socket: &mut TcpSocket<'_>, rt: &'static RuntimeConfig) {
    if rt.mode == DeviceMode::Onboarding {
        send_status_line(
            socket,
            "403 Forbidden",
            b"manual unlock is disabled during onboarding\n",
        )
        .await;
        return;
    }
    log::warn!("http: manual unlock requested by {:?}", socket.remote_endpoint());
    MANUAL_UNLOCK.signal(());
    send_text(socket, "200 OK", b"ok: door pulsed\n").await;
}

/// Case-insensitive scan for `Content-Length: <decimal>` in the header block.
fn parse_content_length(headers: &str) -> Option<u32> {
    for line in headers.lines() {
        if let Some(colon) = line.find(':') {
            let (name, rest) = line.split_at(colon);
            if name.eq_ignore_ascii_case("Content-Length") {
                return rest[1..].trim().parse().ok();
            }
        }
    }
    None
}

/// Send a tiny `text/plain` response with the given status line and body.
async fn send_status_line(socket: &mut TcpSocket<'_>, status: &str, body: &[u8]) {
    send_text(socket, status, body).await;
}

async fn send_text(socket: &mut TcpSocket<'_>, status: &str, body: &[u8]) {
    let mut header: HString<160> = HString::new();
    let _ = write!(
        header,
        "HTTP/1.1 {}\r\n\
         Content-Type: text/plain; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n",
        status,
        body.len()
    );
    let _ = socket.write_all(header.as_bytes()).await;
    let _ = socket.write_all(body).await;
}

/// `GET /swipes` - dump the offline swipe log as CSV.
///
/// Only standalone units populate this log (Conway units upload swipes to
/// the server instead), so it is empty in Conway mode. Returns at most the
/// most-recent 128 entries, oldest-first, as
/// `fob,allowed,uptime_ms` rows. `uptime_ms` is milliseconds since the
/// device booted (no RTC), so timestamps reset across reboots.
async fn send_swipes_page(socket: &mut TcpSocket<'_>) {
    let entries = crate::swipe_log::read_recent::<128>().await;
    let mut body: HString<6144> = HString::new();
    let _ = body.push_str("fob,allowed,uptime_ms\n");
    for e in entries.iter() {
        let _ = write!(body, "{},{},{}\n", e.fob, e.allowed as u8, e.at_ms);
    }
    send_text(socket, "200 OK", body.as_bytes()).await;
}
async fn send_status_page(
    socket: &mut TcpSocket<'_>,
    fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    local_fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
    etag: &Mutex<CriticalSectionRawMutex, HString<64>>,
    last_swipe: &Mutex<CriticalSectionRawMutex, Option<LastSwipe>>,
    stack: &Stack<'static>,
    rt: &'static RuntimeConfig,
) {
    // Gather state.
    let uptime_ms = Instant::now().as_millis();
    let uptime_secs = uptime_ms / 1000;
    let fob_count = fobs.lock().await.len();
    let local_fob_count = local_fobs.lock().await.len();
    let pending_events = EVENT_BUFFER.len().await;
    let current_etag = {
        let g = etag.lock().await;
        g.clone()
    };
    let last_swipe_snap: Option<LastSwipe> = *last_swipe.lock().await;

    // Snapshot live settings so the page reflects current creds and
    // Conway URL even after a /config save (which reboots, but better
    // safe than sorry if we ever support hot-reload).
    let (cur_ssid, conway_host_str, conway_port, conway_enabled, is_onboarding) = {
        let s = rt.settings.lock().await;
        let mut hs: HString<24> = HString::new();
        let _ = hs.push_str(&s.conway_host_str());
        let displayed_ssid: HString<48> = if rt.mode == DeviceMode::Onboarding {
            let mut t: HString<48> = HString::new();
            let _ = t.push_str(rt.ap_ssid.as_str());
            let _ = t.push_str(" (onboarding AP)");
            t
        } else {
            let mut t: HString<48> = HString::new();
            let _ = t.push_str(if s.ssid.is_empty() {
                "(unset)"
            } else {
                s.ssid.as_str()
            });
            t
        };
        (
            displayed_ssid,
            hs,
            s.conway_port,
            s.conway_enabled(),
            rt.mode == DeviceMode::Onboarding,
        )
    };

    let mut ip_str: HString<32> = HString::new();
    if let Some(cfg) = stack.config_v4() {
        let _ = write!(ip_str, "{}", cfg.address);
    } else {
        let _ = ip_str.push_str("n/a");
    }

    let firmware = env!("CARGO_PKG_VERSION");

    // OTA status. If the partition layout is missing we just show a
    // dash instead of failing the whole page.
    let mut ota_str: HString<48> = HString::new();
    let mut next_slot_size: u32 = 0;
    match ota::status() {
        Ok(s) => {
            let _ = write!(
                ota_str,
                "{} (next: {}, {} KiB)",
                ota::slot_label(s.current),
                ota::slot_label(s.current.next()),
                s.next_size / 1024
            );
            next_slot_size = s.next_size;
        }
        Err(_) => {
            let _ = ota_str.push_str("(unavailable)");
        }
    }

    let mut banner: HString<1024> = HString::new();
    if is_onboarding {
        if banner
            .push_str(
                "<p class=\"err\"><b>Onboarding mode.</b> This device is not yet \
                 configured. Open <a href=\"/config\">Configuration</a> to set the \
                 WiFi network and Conway server address.</p>",
            )
            .is_err()
        {
            log::error!("http: banner buffer overflow appending onboarding notice");
        }
    }
    // Surface any staged pubkey-touching config so an operator standing
    // at the device knows a short CONFIG press will commit + reboot
    // rather than the usual "request a sync" semantics.
    {
        let g = PENDING_CONFIG.lock().await;
        if let Some(p) = g.as_ref() {
            let elapsed = Instant::now() - p.created_at;
            if elapsed < PENDING_CONFIG_TTL {
                let remaining = (PENDING_CONFIG_TTL - elapsed).as_secs();
                let mut tmp: HString<256> = HString::new();
                let _ = write!(
                    tmp,
                    "<p class=\"err\"><b>Pending config awaiting confirmation.</b> \
                     A configuration change that touches the trusted signing key \
                     has been staged. Press the CONFIG button within \
                     <b>{}s</b> to commit and reboot, or wait for it to expire.</p>",
                    remaining
                );
                if banner.push_str(tmp.as_str()).is_err() {
                    log::error!("http: banner buffer overflow appending pending notice");
                }
            }
        }
    }
    #[cfg(feature = "esp32")]
    if !crate::device_key::is_ready() {
        if banner
            .push_str(
                "<p class=\"err\"><b>Device key not provisioned.</b> The per-device \
                 eFuse root key is unset (BLOCK3 is all-zero). At-rest encryption \
                 is DISABLED: settings and local fobs cannot be saved. Run \
                 <code>tools/provision-device-key.sh</code> against this unit, \
                 then reboot. See <code>tools/README.md</code>.</p>",
            )
            .is_err()
        {
            log::error!("http: banner buffer overflow appending unprovisioned notice");
        }
    }

    // Format the last-swipe row, e.g.
    //   "fob 1234567 <span class=ok>Granted</span> &middot; 12s ago (manual)"
    // or "(none)" if no swipe has been recorded since boot.
    let mut last_swipe_html: HString<192> = HString::new();
    match last_swipe_snap {
        None => {
            let _ = last_swipe_html.push_str("(none)");
        }
        Some(ls) => {
            let age_ms = uptime_ms.saturating_sub(ls.at_uptime_ms);
            let age_secs = age_ms / 1000;
            let (status_class, status_text) = if ls.allowed {
                ("ok", "Granted")
            } else {
                ("err", "Denied")
            };
            let label = if ls.manual { " (manual)" } else { "" };
            if age_secs < 60 {
                let _ = write!(
                    last_swipe_html,
                    "fob {} &middot; <span class=\"{}\">{}</span> &middot; {}s ago{}",
                    ls.fob, status_class, status_text, age_secs, label
                );
            } else {
                let _ = write!(
                    last_swipe_html,
                    "fob {} &middot; <span class=\"{}\">{}</span> &middot; {}m {}s ago{}",
                    ls.fob,
                    status_class,
                    status_text,
                    age_secs / 60,
                    age_secs % 60,
                    label
                );
            }
        }
    }

    // Manual-unlock button is hidden in onboarding mode (POST /unlock
    // returns 403 there anyway).
    let unlock_section: &str = if is_onboarding {
        ""
    } else {
        "<h2>Door</h2>\
         <p><button id=\"unlockbtn\">Unlock door</button> \
         <span id=\"unlockstatus\"></span></p>"
    };

    // Compose the "Conway server" row: either "host:port" or just
    // "(standalone)" with no port suffix.
    let mut conway_row: HString<48> = HString::new();
    if conway_enabled {
        let _ = write!(conway_row, "{}:{}", conway_host_str.as_str(), conway_port);
    } else {
        let _ = conway_row.push_str(conway_host_str.as_str()); // already "(standalone)"
    }

    // Build body. 5 KiB is plenty for this page including the upload
    // form, last-swipe row, and unlock button.
    let mut body: HString<5120> = HString::new();
    let _ = write!(
        body,
        "<!doctype html>\
<html><head><meta charset=\"utf-8\"><title>Conway Access Controller</title>\
<style>body{{font-family:system-ui,sans-serif;margin:2rem;max-width:40rem}}\
h1{{margin-bottom:0}}h2{{margin-top:2rem}}table{{border-collapse:collapse;margin-top:1rem}}\
th,td{{text-align:left;padding:.25rem .75rem;border-bottom:1px solid #ddd}}\
th{{background:#f3f3f3}}progress{{width:100%}}\
.err{{color:#b00}}.ok{{color:#070}}</style></head><body>\
<h1>Conway Access Controller</h1>\
<p>Firmware v{firmware} &middot; <a href=\"/config\">Configuration</a> &middot; <a href=\"/fobs\">Local fobs</a> &middot; <a href=\"/swipes\">Swipe log</a></p>\
{banner}\
<table>\
<tr><th>Uptime</th><td>{uptime} s</td></tr>\
<tr><th>WiFi SSID</th><td>{ssid}</td></tr>\
<tr><th>IPv4</th><td>{ip}</td></tr>\
<tr><th>Conway server</th><td>{conway_row}</td></tr>\
<tr><th>Cached fobs (Conway)</th><td>{fobs}</td></tr>\
<tr><th>Local fobs</th><td>{local_fobs} (<a href=\"/fobs\">manage</a>)</td></tr>\
<tr title=\"Access decisions buffered locally; flushed to Conway on next sync.\"><th>Pending events (queued for Conway)</th><td>{events}</td></tr>\
<tr><th>Last swipe</th><td>{last_swipe}</td></tr>\
<tr title=\"Opaque token returned by Conway; used to detect changes on next sync.\"><th>Last sync token</th><td>{etag}</td></tr>\
<tr><th>OTA slot</th><td>{ota}</td></tr>\
</table>\
{unlock_section}\
<h2>Firmware update</h2>\
<p>Max image size: {maxk} KiB. The device will reboot into the new \
image on success.</p>\
<form id=\"otaform\">\
<input type=\"file\" id=\"otafile\" accept=\".bin\" required>\
<button type=\"submit\">Upload</button>\
</form>\
<p><progress id=\"otaprog\" value=\"0\" max=\"100\"></progress></p>\
<p id=\"otastatus\"></p>\
<p><button id=\"rollbackbtn\">Roll back to previous slot</button></p>\
<script>\
const f=document.getElementById('otaform'),fi=document.getElementById('otafile'),\
p=document.getElementById('otaprog'),s=document.getElementById('otastatus'),\
rb=document.getElementById('rollbackbtn'),\
ub=document.getElementById('unlockbtn'),\
us=document.getElementById('unlockstatus');\
f.addEventListener('submit',e=>{{e.preventDefault();const file=fi.files[0];if(!file)return;\
s.textContent='Uploading '+file.size+' bytes...';s.className='';\
const x=new XMLHttpRequest();x.open('POST','/ota');\
x.setRequestHeader('Content-Type','application/octet-stream');\
x.upload.onprogress=ev=>{{if(ev.lengthComputable)p.value=ev.loaded/ev.total*100;}};\
x.onload=()=>{{s.textContent=x.responseText||('status '+x.status);\
s.className=x.status===200?'ok':'err';}};\
x.onerror=()=>{{s.textContent='upload failed';s.className='err';}};\
x.send(file);}});\
rb.addEventListener('click',()=>{{if(!confirm('Roll back and reboot?'))return;\
fetch('/ota/rollback',{{method:'POST'}}).then(r=>r.text()).then(t=>{{s.textContent=t;}})\
.catch(e=>{{s.textContent='rollback failed';s.className='err';}});}});\
if(ub){{ub.addEventListener('click',()=>{{if(!confirm('Unlock the door now?'))return;\
us.textContent='unlocking...';us.className='';\
fetch('/unlock',{{method:'POST'}}).then(r=>r.text().then(t=>{{\
us.textContent=t.trim();us.className=r.ok?'ok':'err';\
if(r.ok)setTimeout(()=>location.reload(),800);}}))\
.catch(e=>{{us.textContent='unlock failed';us.className='err';}});}});}}\
</script>\
</body></html>",
        firmware = firmware,
        banner = banner.as_str(),
        uptime = uptime_secs,
        ssid = cur_ssid.as_str(),
        ip = ip_str.as_str(),
        conway_row = conway_row.as_str(),
        fobs = fob_count,
        local_fobs = local_fob_count,
        events = pending_events,
        last_swipe = last_swipe_html.as_str(),
        etag = if current_etag.is_empty() {
            "(none)"
        } else {
            current_etag.as_str()
        },
        ota = ota_str.as_str(),
        maxk = next_slot_size / 1024,
        unlock_section = unlock_section,
    );

    let mut header: HString<160> = HString::new();
    let _ = write!(
        header,
        "HTTP/1.1 200 OK\r\n\
         Content-Type: text/html; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Cache-Control: no-store\r\n\
         Connection: close\r\n\
         \r\n",
        body.len()
    );

    if let Err(e) = socket.write_all(header.as_bytes()).await {
        log::warn!("http: write header failed: {:?}", e);
        return;
    }
    if let Err(e) = socket.write_all(body.as_bytes()).await {
        log::warn!("http: write body failed: {:?}", e);
    }
}

/// Render the configuration form, pre-filled with current settings.
async fn send_config_page(socket: &mut TcpSocket<'_>, rt: &'static RuntimeConfig) {
    let (ssid, password, host_str, port, mode, current_pubkey_b64) = {
        let s = rt.settings.lock().await;
        let mut hs: HString<24> = HString::new();
        if let Some(h) = s.conway_host {
            let _ = write!(hs, "{}.{}.{}.{}", h[0], h[1], h[2], h[3]);
        }
        let pk_b64 = s
            .trusted_pubkey
            .as_ref()
            .map(|k| signing::b64_encode(k))
            .unwrap_or_default();
        (
            s.ssid.clone(),
            s.password.clone(),
            hs,
            s.conway_port,
            rt.mode,
            pk_b64,
        )
    };

    // Snapshot any staged pending pubkey-touching config so the form
    // can surface "already staged, press CONFIG within Xs" instead of
    // silently re-staging on top of itself.
    let pending_remaining_secs: Option<u64> = {
        let g = PENDING_CONFIG.lock().await;
        g.as_ref().and_then(|p| {
            let elapsed = Instant::now() - p.created_at;
            if elapsed < PENDING_CONFIG_TTL {
                Some((PENDING_CONFIG_TTL - elapsed).as_secs())
            } else {
                None
            }
        })
    };

    let banner = if mode == DeviceMode::Onboarding {
        let mut b: HString<256> = HString::new();
        let _ = write!(
            b,
            "<p class=\"info\">You are connected to the onboarding network \
             <b>{}</b>. Fill in your WiFi credentials and Conway server \
             address. The device will save and reboot.</p>",
            rt.ap_ssid.as_str()
        );
        b
    } else {
        HString::new()
    };

    let mut pending_banner: alloc::string::String = alloc::string::String::new();
    if let Some(secs) = pending_remaining_secs {
        let _ = core::fmt::Write::write_fmt(
            &mut pending_banner,
            format_args!(
                "<p class=\"warn\"><b>Pending config awaiting confirmation.</b> \
                 Press the CONFIG button within <b>{} s</b> to commit. \
                 Submitting again before then will overwrite the staged value.</p>",
                secs
            ),
        );
    }

    // When the per-device eFuse root key is unset, at-rest encryption is
    // disabled and `settings::save` will fail. Surface this on the config
    // page itself (not just /status) so onboarding does not dead-end on a
    // bare 500 after the operator fills in and submits the form.
    let unprovisioned_banner: alloc::string::String = {
        #[cfg(feature = "esp32")]
        {
            if !crate::device_key::is_ready() {
                alloc::string::String::from(
                    "<p class=\"err\"><b>Device not provisioned &mdash; Save will fail.</b> \
                     The per-device eFuse root key is unset (BLOCK3 is all-zero), so \
                     at-rest encryption is disabled and settings cannot be saved. \
                     Provision the unit first: run <code>tools/provision-device-key.sh</code> \
                     against it, then reboot. See <code>tools/README.md</code>.</p>",
                )
            } else {
                alloc::string::String::new()
            }
        }
        #[cfg(not(feature = "esp32"))]
        {
            alloc::string::String::new()
        }
    };

    let mut body: alloc::string::String = alloc::string::String::with_capacity(4096);
    let mut esc_ssid: HString<128> = HString::new();
    html_escape_into(&ssid, &mut esc_ssid);
    // SECURITY: never echo the stored WiFi password back into the form. Any
    // unauthenticated LAN client can otherwise read the cleartext PSK via
    // view-source. Render an empty field with a placeholder instead; a blank
    // submission is treated as "keep current" in handle_config_post.
    let pw_placeholder: &str = if password.is_empty() {
        ""
    } else {
        "leave blank to keep current password"
    };

    let pubkey_status: alloc::string::String = if current_pubkey_b64.is_empty() {
        alloc::string::String::from(
            "<i>not set</i> &mdash; responses from the Conway server are accepted unsigned (back-compat).",
        )
    } else {
        let mut s = alloc::string::String::with_capacity(current_pubkey_b64.len() + 64);
        s.push_str("<code style=\"word-break:break-all\">");
        s.push_str(&current_pubkey_b64);
        s.push_str("</code>");
        s
    };

    // The Ed25519 signing-key controls are an advanced, rarely-touched
    // setting. Keep onboarding minimal by omitting them entirely while in
    // Onboarding mode, and tuck them behind an <details> "Advanced"
    // disclosure once the device is configured.
    let advanced_section: alloc::string::String = if mode == DeviceMode::Onboarding {
        alloc::string::String::new()
    } else {
        let mut s = alloc::string::String::with_capacity(1024);
        let _ = core::fmt::Write::write_fmt(
            &mut s,
            format_args!(
                "<details style=\"margin-top:1.5rem\">\
<summary style=\"cursor:pointer;font-weight:600\">Advanced</summary>\
<fieldset>\
<legend>Response signing key (Ed25519)</legend>\
<p>Current trusted key: {pubkey_status}</p>\
<label>Set / replace key (standard base64, 44 chars)<input type=\"text\" name=\"trusted_pubkey\" value=\"\" maxlength=\"64\" placeholder=\"leave blank for no change\"></label>\
<label style=\"font-weight:normal;margin-top:.75rem\"><input type=\"checkbox\" name=\"clear_pubkey\" value=\"1\" style=\"width:auto;margin-right:.5rem\">Clear the trusted key (disables signature enforcement)</label>\
<p class=\"note\"><b>Any change to this field requires physical confirmation.</b> \
After submitting, you have {ttl}s to press the CONFIG button on the device to commit. \
Other fields above are saved <em>only</em> on that physical confirmation when this field changes; \
when this field is left untouched they save immediately and the device reboots.</p>\
</fieldset>\
</details>",
                pubkey_status = pubkey_status,
                ttl = PENDING_CONFIG_TTL.as_secs(),
            ),
        );
        s
    };

    let _ = core::fmt::Write::write_fmt(
        &mut body,
        format_args!(
            "<!doctype html>\
<html><head><meta charset=\"utf-8\"><title>Conway Configuration</title>\
<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\
<style>body{{font-family:system-ui,sans-serif;margin:2rem;max-width:32rem}}\
label{{display:block;margin-top:1rem;font-weight:600}}\
input[type=text],input[type=password],input[type=number]{{width:100%;padding:.5rem;font-size:1rem;box-sizing:border-box;margin-top:.25rem}}\
button{{margin-top:1.5rem;padding:.6rem 1.2rem;font-size:1rem}}\
.info{{padding:.75rem;background:#eef;border-left:4px solid #44a;border-radius:4px}}\
.warn{{padding:.75rem;background:#ffe;border-left:4px solid #b80;border-radius:4px;margin:1rem 0}}\
.err{{color:#b00}}.row{{display:flex;gap:.5rem}}.row>div:first-child{{flex:3}}.row>div:last-child{{flex:1}}\
fieldset{{margin-top:1.5rem;padding:.75rem 1rem;border:1px solid #ccc;border-radius:4px}}\
fieldset legend{{font-weight:600;padding:0 .5rem}}\
.note{{font-size:.85rem;color:#555;margin-top:.5rem}}\
</style></head><body>\
<h1>Conway Configuration</h1>\
{unprov}\
{banner}\
{pending}\
<form method=\"POST\" action=\"/config\">\
<label>WiFi SSID<input type=\"text\" name=\"ssid\" value=\"{ssid}\" maxlength=\"{max_ssid}\" required></label>\
<label>WiFi Password<input type=\"password\" name=\"password\" value=\"\" maxlength=\"{max_pw}\" placeholder=\"{pw_ph}\"><label style=\"display:inline;font-weight:normal;font-size:.9rem\"><input type=\"checkbox\" style=\"width:auto;margin-right:.25rem\" onclick=\"this.parentElement.previousElementSibling.type=this.checked?'text':'password'\"> Show</label></label>\
<div class=\"row\">\
<div><label>Conway Host (IPv4, blank for standalone)<input type=\"text\" name=\"host\" value=\"{host}\" pattern=\"|[0-9.]+\"></label></div>\
<div><label>Port<input type=\"number\" name=\"port\" value=\"{port}\" min=\"1\" max=\"65535\" required></label></div>\
</div>\
<p class=\"note\">Leave Conway Host blank to operate standalone. Only locally-added fobs will be accepted; events are not buffered.</p>\
{advanced}\
<button type=\"submit\">Save</button>\
</form>\
<p style=\"margin-top:2rem\"><a href=\"/status\">Back to status</a></p>\
</body></html>",
            banner = banner.as_str(),
            pending = pending_banner.as_str(),
            unprov = unprovisioned_banner.as_str(),
            ssid = esc_ssid.as_str(),
            pw_ph = pw_placeholder,
            host = host_str.as_str(),
            port = port,
            max_ssid = MAX_SSID,
            max_pw = MAX_PASSWORD,
            advanced = advanced_section.as_str(),
        ),
    );

    let mut header: HString<160> = HString::new();
    let _ = write!(
        header,
        "HTTP/1.1 200 OK\r\n\
         Content-Type: text/html; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Cache-Control: no-store\r\n\
         Connection: close\r\n\
         \r\n",
        body.len()
    );
    let _ = socket.write_all(header.as_bytes()).await;
    let _ = socket.write_all(body.as_bytes()).await;
}

/// Receive a urlencoded config form, validate, then either persist
/// immediately (and reboot) or — when the submission touches the
/// `trusted_pubkey` field — stage the full new [`Settings`] in
/// [`PENDING_CONFIG`] and render a confirmation page asking the
/// operator to press the CONFIG button within [`PENDING_CONFIG_TTL`].
async fn handle_config_post(
    socket: &mut TcpSocket<'_>,
    content_length: u32,
    leftover: &[u8],
    rt: &'static RuntimeConfig,
) {
    let mut body: alloc::vec::Vec<u8> =
        alloc::vec::Vec::with_capacity(content_length as usize);
    body.extend_from_slice(leftover);
    while body.len() < content_length as usize {
        let mut chunk = [0u8; 256];
        let want = (content_length as usize - body.len()).min(chunk.len());
        match socket.read(&mut chunk[..want]).await {
            Ok(0) => {
                send_config_error(socket, "400 Bad Request", "short body").await;
                return;
            }
            Ok(n) => body.extend_from_slice(&chunk[..n]),
            Err(_) => {
                send_config_error(socket, "400 Bad Request", "read error").await;
                return;
            }
        }
    }

    let body_str = match core::str::from_utf8(&body) {
        Ok(s) => s,
        Err(_) => {
            send_config_error(socket, "400 Bad Request", "invalid utf-8").await;
            return;
        }
    };

    let mut ssid: alloc::string::String = alloc::string::String::new();
    let mut password: alloc::string::String = alloc::string::String::new();
    let mut host: alloc::string::String = alloc::string::String::new();
    let mut port_str: alloc::string::String = alloc::string::String::new();
    let mut trusted_pubkey_str: alloc::string::String = alloc::string::String::new();
    let mut clear_pubkey: bool = false;

    for pair in body_str.split('&') {
        let (k, v) = match pair.split_once('=') {
            Some(kv) => kv,
            None => continue,
        };
        let decoded = match urldecode(v) {
            Some(d) => d,
            None => {
                send_config_error(socket, "400 Bad Request", "bad urlencoding").await;
                return;
            }
        };
        match k {
            "ssid" => ssid = decoded,
            "password" => password = decoded,
            "host" => host = decoded,
            "port" => port_str = decoded,
            "trusted_pubkey" => trusted_pubkey_str = decoded,
            "clear_pubkey" => clear_pubkey = decoded == "1" || decoded == "on",
            _ => {}
        }
    }

    if ssid.is_empty() || ssid.len() > MAX_SSID {
        send_config_error(socket, "400 Bad Request", "ssid empty or too long").await;
        return;
    }
    if password.len() > MAX_PASSWORD {
        send_config_error(socket, "400 Bad Request", "password too long").await;
        return;
    }
    // The config form never echoes the stored WiFi password back (so an
    // unauthenticated LAN client cannot read the PSK via view-source), so a
    // blank password field means "keep the currently stored password" rather
    // than overwriting it with an empty value.
    let password = if password.is_empty() {
        rt.settings.lock().await.password.clone()
    } else {
        password
    };
    let host_octets = if host.is_empty() {
        // Standalone mode: no Conway server.
        None
    } else {
        match settings::parse_ipv4(&host) {
            Some(o) => Some(o),
            None => {
                send_config_error(
                    socket,
                    "400 Bad Request",
                    "host must be dotted-quad IPv4 (or blank for standalone)",
                )
                .await;
                return;
            }
        }
    };
    let port: u16 = match port_str.parse() {
        Ok(p) if p > 0 => p,
        _ => {
            send_config_error(socket, "400 Bad Request", "invalid port").await;
            return;
        }
    };

    // Resolve the pubkey field. Precedence:
    //   1. non-empty `trusted_pubkey` -> stage Some(new_key)
    //   2. clear_pubkey=1             -> stage None
    //   3. otherwise                   -> preserve current pubkey, no
    //                                    staging required (legacy path).
    let trusted_trimmed = trusted_pubkey_str.trim();
    let current_pubkey = rt.settings.lock().await.trusted_pubkey;

    enum PubkeyChange {
        Set([u8; 32]),
        Clear,
        Unchanged,
    }
    let change = if !trusted_trimmed.is_empty() {
        match signing::b64_decode(trusted_trimmed) {
            Some(v) if v.len() == 32 => {
                let mut k = [0u8; 32];
                k.copy_from_slice(&v);
                PubkeyChange::Set(k)
            }
            Some(_) => {
                send_config_error(
                    socket,
                    "400 Bad Request",
                    "trusted_pubkey must decode to exactly 32 bytes (Ed25519 public key)",
                )
                .await;
                return;
            }
            None => {
                send_config_error(
                    socket,
                    "400 Bad Request",
                    "trusted_pubkey is not valid base64",
                )
                .await;
                return;
            }
        }
    } else if clear_pubkey {
        PubkeyChange::Clear
    } else {
        PubkeyChange::Unchanged
    };

    let new_pubkey = match change {
        PubkeyChange::Set(k) => Some(k),
        PubkeyChange::Clear => None,
        PubkeyChange::Unchanged => current_pubkey,
    };

    let new = Settings {
        ssid,
        password,
        conway_host: host_octets,
        conway_port: port,
        trusted_pubkey: new_pubkey,
    };

    let requires_confirmation = matches!(change, PubkeyChange::Set(_) | PubkeyChange::Clear);

    if requires_confirmation {
        // Stage the FULL new Settings (not just the pubkey delta) so
        // the commit on physical press is atomic with respect to any
        // other fields the user also changed in the same submit.
        {
            let mut g = PENDING_CONFIG.lock().await;
            *g = Some(PendingConfig {
                settings: new,
                created_at: Instant::now(),
            });
        }
        let action_word = match change {
            PubkeyChange::Set(_) => "install a new trusted signing key",
            PubkeyChange::Clear => "clear the trusted signing key",
            PubkeyChange::Unchanged => unreachable!(),
        };
        let mut resp = alloc::string::String::with_capacity(512);
        let _ = core::fmt::Write::write_fmt(
            &mut resp,
            format_args!(
                "<!doctype html><html><head><meta charset=\"utf-8\">\
<title>Confirm on device</title>\
<meta http-equiv=\"refresh\" content=\"{ttl}; url=/config\">\
<style>body{{font-family:system-ui,sans-serif;margin:2rem;max-width:32rem}}\
.warn{{padding:1rem;background:#ffe;border-left:4px solid #b80;border-radius:4px}}\
</style></head><body>\
<h1>Press CONFIG to confirm</h1>\
<p class=\"warn\">This change will <b>{action}</b>. Because this affects the \
device's trust anchor for the Conway server, it requires physical confirmation \
to defend against silent LAN-side reconfiguration.</p>\
<p><b>Press the CONFIG button on the device within {ttl} seconds.</b> \
On confirmation, the device will save all settings from this form and reboot. \
If you do not press the button in time, the staged change is discarded and \
nothing is written.</p>\
<p>This page will auto-refresh to <a href=\"/config\">/config</a> after the \
window expires.</p>\
</body></html>",
                action = action_word,
                ttl = PENDING_CONFIG_TTL.as_secs(),
            ),
        );
        send_html(socket, "202 Accepted", resp.as_bytes()).await;
        log::warn!(
            "config: staged pubkey-touching config, awaiting CONFIG button (TTL {}s)",
            PENDING_CONFIG_TTL.as_secs()
        );
        return;
    }

    // Legacy fast path: no pubkey change, save immediately and reboot.
    {
        let mut guard = rt.settings.lock().await;
        *guard = new.clone();
    }

    WATCHDOG_FEED.signal(());

    if let Err(e) = settings::save(&new) {
        log::error!("config: save failed: {}", e);
        let mut msg: alloc::string::String = alloc::string::String::with_capacity(256);
        let _ = core::fmt::Write::write_fmt(
            &mut msg,
            format_args!(
                "Could not save settings: {}. If this device is not yet provisioned, \
                 run tools/provision-device-key.sh against it and reboot before \
                 onboarding (see tools/README.md).",
                e
            ),
        );
        send_config_error(socket, "500 Internal Server Error", &msg).await;
        return;
    }

    let resp = b"<!doctype html><html><body><h1>Saved</h1>\
        <p>Settings stored. The device will reboot in 2 seconds and try \
        to join the new network. If it doesn't come back online, hold the \
        CONFIG button for 5 seconds to factory-reset.</p>\
        <p>After reboot the device joins your WiFi via DHCP \xe2\x80\x94 it appears \
        as host <code>conway-XXXXXX</code> in your router's DHCP lease table; the \
        status page will be at <code>http://&lt;ip&gt;/</code>.</p></body></html>";
    send_html(socket, "200 OK", resp).await;
    let _ = socket.flush().await;
    socket.close();

    log::warn!("config: settings saved, rebooting");
    Timer::after(Duration::from_secs(2)).await;
    esp_hal::system::software_reset();
}

/// Decode application/x-www-form-urlencoded.
fn urldecode(input: &str) -> Option<alloc::string::String> {
    let bytes = input.as_bytes();
    let mut out: alloc::vec::Vec<u8> = alloc::vec::Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'+' => {
                out.push(b' ');
                i += 1;
            }
            b'%' => {
                if i + 2 >= bytes.len() {
                    return None;
                }
                let hi = hex_nibble(bytes[i + 1])?;
                let lo = hex_nibble(bytes[i + 2])?;
                out.push((hi << 4) | lo);
                i += 3;
            }
            b => {
                out.push(b);
                i += 1;
            }
        }
    }
    alloc::string::String::from_utf8(out).ok()
}

fn hex_nibble(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

/// Append `src` to `dst` with HTML-attribute-safe escaping. Silently
/// truncates if `dst` would overflow.
fn html_escape_into<const N: usize>(src: &str, dst: &mut HString<N>) {
    for c in src.chars() {
        let r: &str = match c {
            '&' => "&amp;",
            '<' => "&lt;",
            '>' => "&gt;",
            '"' => "&quot;",
            '\'' => "&#39;",
            _ => {
                let mut tmp = [0u8; 4];
                let s = c.encode_utf8(&mut tmp);
                if dst.push_str(s).is_err() {
                    return;
                }
                continue;
            }
        };
        if dst.push_str(r).is_err() {
            return;
        }
    }
}

/// Send an HTML-wrapped configuration error page (used by `POST /config`
/// validation failures) so users get a styled message with a link back to
/// the form, not bare plaintext.
async fn send_config_error(socket: &mut TcpSocket<'_>, status: &str, message: &str) {
    let mut esc = alloc::string::String::with_capacity(message.len() + 16);
    html_escape_str(message, &mut esc);
    let mut body = alloc::string::String::with_capacity(esc.len() + 192);
    let _ = core::fmt::Write::write_fmt(
        &mut body,
        format_args!(
            "<!doctype html><meta charset=utf-8><title>Configuration error</title>\
<h1>Configuration error</h1><p>{}</p><p><a href=\"/config\">Back to form</a></p>",
            esc
        ),
    );
    send_html(socket, status, body.as_bytes()).await;
}

async fn send_redirect(socket: &mut TcpSocket<'_>, location: &str) {
    let mut esc_loc = alloc::string::String::with_capacity(location.len());
    html_escape_str(location, &mut esc_loc);
    let mut body = alloc::string::String::with_capacity(esc_loc.len() + 32);
    let _ = core::fmt::Write::write_fmt(
        &mut body,
        format_args!("<a href=\"{}\">Continue</a>\n", esc_loc),
    );
    let mut header: HString<256> = HString::new();
    let _ = write!(
        header,
        "HTTP/1.1 302 Found\r\n\
         Location: {}\r\n\
         Content-Type: text/html; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Cache-Control: no-store\r\n\
         Connection: close\r\n\
         \r\n",
        location,
        body.len()
    );
    let _ = socket.write_all(header.as_bytes()).await;
    let _ = socket.write_all(body.as_bytes()).await;
}

/// HTML-escape `src` into an `alloc::string::String`.
fn html_escape_str(src: &str, dst: &mut alloc::string::String) {
    for c in src.chars() {
        match c {
            '&' => dst.push_str("&amp;"),
            '<' => dst.push_str("&lt;"),
            '>' => dst.push_str("&gt;"),
            '"' => dst.push_str("&quot;"),
            '\'' => dst.push_str("&#39;"),
            _ => dst.push(c),
        }
    }
}

async fn send_html(socket: &mut TcpSocket<'_>, status: &str, body: &[u8]) {
    let mut header: HString<160> = HString::new();
    let _ = write!(
        header,
        "HTTP/1.1 {}\r\n\
         Content-Type: text/html; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n",
        status,
        body.len()
    );
    let _ = socket.write_all(header.as_bytes()).await;
    let _ = socket.write_all(body).await;
}

/// Find the position just past the `\r\n\r\n` that terminates an HTTP header
/// block. Returns the index of the first byte AFTER the terminator, or `None`.
fn find_double_crlf(buf: &[u8]) -> Option<usize> {
    buf.windows(4).position(|w| w == b"\r\n\r\n").map(|p| p + 4)
}

// ----------------------------------------------------------------------------
// /fobs - local fob management UI.
// ----------------------------------------------------------------------------

/// Render the local fob list with an "add" form and per-row delete buttons.
///
/// The list can grow up to `MAX_LOCAL_FOBS` (128) entries; rendered HTML is
/// dynamically sized via `alloc::string::String` so we never need to size a
/// large `HString` on the stack.
async fn send_fobs_page(
    socket: &mut TcpSocket<'_>,
    local_fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
    rt: &'static RuntimeConfig,
) {
    // Snapshot the list into an alloc Vec so we release the mutex before
    // any (potentially long) socket writes.
    let snapshot: alloc::vec::Vec<LocalFob> = {
        let g = local_fobs.lock().await;
        g.iter().cloned().collect()
    };
    let count = snapshot.len();
    let conway_enabled = rt.settings.lock().await.conway_enabled();

    let mut body = alloc::string::String::with_capacity(2048 + count * 96);
    body.push_str(
        "<!doctype html><html><head><meta charset=\"utf-8\">\
<title>Local Fobs</title>\
<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\
<style>body{font-family:system-ui,sans-serif;margin:2rem;max-width:40rem}\
table{border-collapse:collapse;margin-top:1rem;width:100%}\
th,td{text-align:left;padding:.4rem .6rem;border-bottom:1px solid #ddd}\
th{background:#f3f3f3}input{padding:.4rem;font-size:1rem}\
.add{display:flex;gap:.5rem;align-items:end;margin-top:1rem}\
.add label{display:flex;flex-direction:column;font-size:.9rem}\
button{padding:.4rem .8rem;font-size:1rem}\
.info{padding:.6rem;background:#eef;border-left:4px solid #44a;border-radius:4px;margin-bottom:1rem}\
.del{background:#fdd;border:1px solid #b00;color:#b00}\
form.inline{display:inline}</style></head><body>\
<h1>Local Fobs</h1>\
<p><a href=\"/status\">&larr; Status</a></p>",
    );
    if !conway_enabled {
        body.push_str(
            "<div class=\"info\"><b>Standalone mode.</b> No Conway server is configured; \
this list is the sole source of authorization.</div>",
        );
    } else {
        body.push_str(
            "<div class=\"info\">Local fobs always grant access. They are checked \
before the Conway-synced cache. Local entries can only <em>grant</em> access; \
to revoke a Conway-issued fob, disable it in Conway.</div>",
        );
    }
    let _ = core::fmt::Write::write_fmt(
        &mut body,
        format_args!("<p>{} / {} entries.</p>", count, MAX_LOCAL_FOBS),
    );

    body.push_str(
        "<form method=\"POST\" action=\"/fobs\" class=\"add\">\
<label>Fob ID<input name=\"id\" type=\"number\" min=\"1\" max=\"4294967295\" required></label>\
<label>Label<input name=\"label\" maxlength=\"16\" placeholder=\"e.g. Alice\"></label>\
<button type=\"submit\">Add</button></form>",
    );

    if count == 0 {
        body.push_str("<p><i>No local fobs yet.</i></p>");
    } else {
        body.push_str("<table><tr><th>ID</th><th>Label</th><th></th></tr>");
        for f in snapshot.iter() {
            // Escape label for HTML safety.
            let mut esc: alloc::string::String = alloc::string::String::with_capacity(f.label.len());
            for c in f.label.chars() {
                match c {
                    '&' => esc.push_str("&amp;"),
                    '<' => esc.push_str("&lt;"),
                    '>' => esc.push_str("&gt;"),
                    '"' => esc.push_str("&quot;"),
                    '\'' => esc.push_str("&#39;"),
                    _ => esc.push(c),
                }
            }
            let _ = core::fmt::Write::write_fmt(
                &mut body,
                format_args!(
                    "<tr><td>{id}</td><td>{label}</td>\
<td><form method=\"POST\" action=\"/fobs/delete\" class=\"inline\" \
onsubmit=\"return confirm('Delete fob {id}?')\">\
<input type=\"hidden\" name=\"id\" value=\"{id}\">\
<button class=\"del\" type=\"submit\">Delete</button></form></td></tr>",
                    id = f.id,
                    label = esc,
                ),
            );
        }
        body.push_str("</table>");
    }
    body.push_str("</body></html>");

    let mut header: HString<160> = HString::new();
    let _ = write!(
        header,
        "HTTP/1.1 200 OK\r\n\
         Content-Type: text/html; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Cache-Control: no-store\r\n\
         Connection: close\r\n\
         \r\n",
        body.len()
    );
    let _ = socket.write_all(header.as_bytes()).await;
    let _ = socket.write_all(body.as_bytes()).await;
}

/// Read the full body of a small urlencoded form post into a Vec.
async fn read_form_body(
    socket: &mut TcpSocket<'_>,
    content_length: u32,
    leftover: &[u8],
) -> Option<alloc::vec::Vec<u8>> {
    let mut body: alloc::vec::Vec<u8> = alloc::vec::Vec::with_capacity(content_length as usize);
    body.extend_from_slice(leftover);
    while body.len() < content_length as usize {
        let mut chunk = [0u8; 256];
        let want = (content_length as usize - body.len()).min(chunk.len());
        match socket.read(&mut chunk[..want]).await {
            Ok(0) => return None,
            Ok(n) => body.extend_from_slice(&chunk[..n]),
            Err(_) => return None,
        }
    }
    Some(body)
}

async fn handle_fob_add(
    socket: &mut TcpSocket<'_>,
    content_length: u32,
    leftover: &[u8],
    local_fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
) {
    let body = match read_form_body(socket, content_length, leftover).await {
        Some(b) => b,
        None => {
            send_status_line(socket, "400 Bad Request", b"short body\n").await;
            return;
        }
    };
    let body_str = match core::str::from_utf8(&body) {
        Ok(s) => s,
        Err(_) => {
            send_status_line(socket, "400 Bad Request", b"invalid utf-8\n").await;
            return;
        }
    };

    let mut id_str = alloc::string::String::new();
    let mut label = alloc::string::String::new();
    for pair in body_str.split('&') {
        let (k, v) = match pair.split_once('=') {
            Some(kv) => kv,
            None => continue,
        };
        let decoded = match urldecode(v) {
            Some(d) => d,
            None => {
                send_status_line(socket, "400 Bad Request", b"bad urlencoding\n").await;
                return;
            }
        };
        match k {
            "id" => id_str = decoded,
            "label" => label = decoded,
            _ => {}
        }
    }

    let id: u32 = match id_str.trim().parse() {
        Ok(n) if n > 0 => n,
        _ => {
            send_status_line(socket, "400 Bad Request", b"id must be a positive integer\n").await;
            return;
        }
    };
    if label.len() > MAX_LABEL_LEN {
        send_status_line(socket, "400 Bad Request", b"label too long (max 16 bytes)\n").await;
        return;
    }
    let mut label_hs: HString<MAX_LABEL_LEN> = HString::new();
    if label_hs.push_str(&label).is_err() {
        send_status_line(socket, "400 Bad Request", b"label too long\n").await;
        return;
    }

    // Update in-memory list, then persist. We swap-replace any existing
    // entry with the same id (so the form doubles as "rename").
    let to_save: alloc::vec::Vec<LocalFob> = {
        let mut g = local_fobs.lock().await;
        if let Some(existing) = g.iter_mut().find(|f| f.id == id) {
            existing.label = label_hs.clone();
        } else if g
            .push(LocalFob {
                id,
                label: label_hs.clone(),
            })
            .is_err()
        {
            send_status_line(
                socket,
                "507 Insufficient Storage",
                b"local fob list is full\n",
            )
            .await;
            return;
        }
        g.iter().cloned().collect()
    };

    WATCHDOG_FEED.signal(());
    if let Err(e) = fob_store::save(&to_save) {
        log::error!("fobs: save failed: {}", e);
        let mut msg: HString<96> = HString::new();
        let _ = write!(msg, "save failed: {}\n", e);
        send_text(socket, "500 Internal Server Error", msg.as_bytes()).await;
        return;
    }

    log::info!("fobs: added id={} label={:?}", id, label_hs.as_str());
    send_redirect(socket, "/fobs").await;
}

async fn handle_fob_delete(
    socket: &mut TcpSocket<'_>,
    content_length: u32,
    leftover: &[u8],
    local_fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<LocalFob, MAX_LOCAL_FOBS>>,
) {
    let body = match read_form_body(socket, content_length, leftover).await {
        Some(b) => b,
        None => {
            send_status_line(socket, "400 Bad Request", b"short body\n").await;
            return;
        }
    };
    let body_str = match core::str::from_utf8(&body) {
        Ok(s) => s,
        Err(_) => {
            send_status_line(socket, "400 Bad Request", b"invalid utf-8\n").await;
            return;
        }
    };

    let mut id_str = alloc::string::String::new();
    for pair in body_str.split('&') {
        if let Some((k, v)) = pair.split_once('=') {
            if k == "id" {
                id_str = urldecode(v).unwrap_or_default();
            }
        }
    }
    let id: u32 = match id_str.trim().parse() {
        Ok(n) => n,
        Err(_) => {
            send_status_line(socket, "400 Bad Request", b"id must be a positive integer\n").await;
            return;
        }
    };

    let (removed, to_save) = {
        let mut g = local_fobs.lock().await;
        let before = g.len();
        // heapless Vec has no retain - rebuild.
        let mut new: heapless::Vec<LocalFob, MAX_LOCAL_FOBS> = heapless::Vec::new();
        for f in g.iter() {
            if f.id != id {
                let _ = new.push(f.clone());
            }
        }
        let removed = before != new.len();
        *g = new;
        (removed, g.iter().cloned().collect::<alloc::vec::Vec<_>>())
    };

    if !removed {
        log::warn!("fobs: delete id={} not found", id);
    } else {
        WATCHDOG_FEED.signal(());
        if let Err(e) = fob_store::save(&to_save) {
            log::error!("fobs: save failed: {}", e);
            let mut msg: HString<96> = HString::new();
            let _ = write!(msg, "save failed: {}\n", e);
            send_text(socket, "500 Internal Server Error", msg.as_bytes()).await;
            return;
        }
        log::info!("fobs: deleted id={}", id);
    }
    send_redirect(socket, "/fobs").await;
}

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

use crate::ota::{self, OtaError, OtaWriter};
use crate::{EVENT_BUFFER, MAX_FOBS, SSID, WATCHDOG_FEED};

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

/// HTTP server task. Runs forever, accepting one connection at a time.
#[embassy_executor::task]
pub async fn http_server_task(
    stack: &'static Stack<'static>,
    fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &'static Mutex<CriticalSectionRawMutex, HString<64>>,
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

        handle_connection(&mut socket, fobs, etag, stack).await;

        let _ = socket.flush().await;
        socket.close();
    }
}

async fn handle_connection(
    socket: &mut TcpSocket<'_>,
    fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &Mutex<CriticalSectionRawMutex, HString<64>>,
    stack: &Stack<'static>,
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
        ("GET", "/") | ("GET", "/status") => {
            send_status_page(socket, fobs, etag, stack).await;
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

/// Render and send the HTML status page.
async fn send_status_page(
    socket: &mut TcpSocket<'_>,
    fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &Mutex<CriticalSectionRawMutex, HString<64>>,
    stack: &Stack<'static>,
) {
    // Gather state.
    let uptime_secs = Instant::now().as_millis() / 1000;
    let fob_count = fobs.lock().await.len();
    let pending_events = EVENT_BUFFER.len().await;
    let current_etag = {
        let g = etag.lock().await;
        g.clone()
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

    // Build body. 4 KiB is plenty for this page including the upload form.
    let mut body: HString<4096> = HString::new();
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
<p>Firmware v{firmware}</p>\
<table>\
<tr><th>Uptime</th><td>{uptime} s</td></tr>\
<tr><th>WiFi SSID</th><td>{ssid}</td></tr>\
<tr><th>IPv4</th><td>{ip}</td></tr>\
<tr><th>Cached fobs</th><td>{fobs}</td></tr>\
<tr><th>Pending events</th><td>{events}</td></tr>\
<tr><th>Sync ETag</th><td>{etag}</td></tr>\
<tr><th>OTA slot</th><td>{ota}</td></tr>\
</table>\
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
rb=document.getElementById('rollbackbtn');\
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
</script>\
</body></html>",
        firmware = firmware,
        uptime = uptime_secs,
        ssid = SSID,
        ip = ip_str.as_str(),
        fobs = fob_count,
        events = pending_events,
        etag = if current_etag.is_empty() {
            "(none)"
        } else {
            current_etag.as_str()
        },
        ota = ota_str.as_str(),
        maxk = next_slot_size / 1024,
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

/// Find the position just past the `\r\n\r\n` that terminates an HTTP header
/// block. Returns the index of the first byte AFTER the terminator, or `None`.
fn find_double_crlf(buf: &[u8]) -> Option<usize> {
    buf.windows(4).position(|w| w == b"\r\n\r\n").map(|p| p + 4)
}

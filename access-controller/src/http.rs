//! Minimal HTTP server.
//!
//! Single-connection accept loop bound to TCP/80. Serves a small HTML status
//! page at `GET /` and `GET /status`; everything else returns 404.
//!
//! Intentionally minimal: no keep-alive, no auth, no TLS, no concurrent
//! connections. This is a foundation for richer endpoints later.

use core::fmt::Write as FmtWrite;
use embassy_net::tcp::TcpSocket;
use embassy_net::Stack;
use embassy_sync::blocking_mutex::raw::CriticalSectionRawMutex;
use embassy_sync::mutex::Mutex;
use embassy_time::{Duration, Instant};
use embedded_io_async::Write;
use heapless::String as HString;

use crate::{EVENT_BUFFER, MAX_FOBS, SSID};

const HTTP_PORT: u16 = 80;
const IO_TIMEOUT: Duration = Duration::from_secs(5);
const REQ_BUF_LEN: usize = 1024;

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
    // connection. The socket itself is re-created per connection so its
    // internal state is always clean.
    let mut rx_buf = [0u8; 1024];
    let mut tx_buf = [0u8; 2048];

    loop {
        let mut socket = TcpSocket::new(*stack, &mut rx_buf, &mut tx_buf);
        socket.set_timeout(Some(IO_TIMEOUT));

        log::debug!("http: waiting for connection");
        if let Err(e) = socket.accept(HTTP_PORT).await {
            log::warn!("http: accept failed: {:?}", e);
            // Brief pause before retrying to avoid tight error loops.
            embassy_time::Timer::after(Duration::from_millis(100)).await;
            continue;
        }

        let peer = socket.remote_endpoint();
        log::info!("http: connection from {:?}", peer);

        handle_connection(&mut socket, fobs, etag, stack).await;

        // Half-close and drain. Ignore errors - peer may have already closed.
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
    // TcpSocket has inherent read/write methods - no trait import needed here.
    // (embedded_io_async::Write is brought into scope at module level for write_all.)

    // Read until we have the request headers (terminated by \r\n\r\n).
    let mut buf = [0u8; REQ_BUF_LEN];
    let mut len = 0usize;
    let header_end = loop {
        if len == buf.len() {
            // Headers too large; bail out with 431-ish behavior (we just close).
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
    let request_line = match core::str::from_utf8(&buf[..header_end]) {
        Ok(s) => s.lines().next().unwrap_or(""),
        Err(_) => {
            send_status_line(socket, "400 Bad Request", b"invalid utf-8\n").await;
            return;
        }
    };

    let mut parts = request_line.split(' ');
    let method = parts.next().unwrap_or("");
    let target = parts.next().unwrap_or("");

    log::info!("http: {} {}", method, target);

    // Strip query string for routing.
    let path = target.split('?').next().unwrap_or("");

    match (method, path) {
        ("GET", "/") | ("GET", "/status") => {
            send_status_page(socket, fobs, etag, stack).await;
        }
        ("GET", _) => {
            send_status_line(socket, "404 Not Found", b"not found\n").await;
        }
        _ => {
            send_status_line(socket, "405 Method Not Allowed", b"method not allowed\n").await;
        }
    }
}

/// Send a tiny `text/plain` response with the given status line and body.
async fn send_status_line(socket: &mut TcpSocket<'_>, status: &str, body: &[u8]) {
    let mut header: HString<128> = HString::new();
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

    // IPv4 address as a short string, or "n/a" if no lease.
    let mut ip_str: HString<32> = HString::new();
    if let Some(cfg) = stack.config_v4() {
        let _ = write!(ip_str, "{}", cfg.address);
    } else {
        let _ = ip_str.push_str("n/a");
    }

    let firmware = env!("CARGO_PKG_VERSION");

    // Build body. 2 KiB is plenty for this page.
    let mut body: HString<2048> = HString::new();
    let _ = write!(
        body,
        "<!doctype html>\
<html><head><meta charset=\"utf-8\"><title>Conway Access Controller</title>\
<style>body{{font-family:system-ui,sans-serif;margin:2rem;max-width:40rem}}\
h1{{margin-bottom:0}}table{{border-collapse:collapse;margin-top:1rem}}\
th,td{{text-align:left;padding:.25rem .75rem;border-bottom:1px solid #ddd}}\
th{{background:#f3f3f3}}</style></head><body>\
<h1>Conway Access Controller</h1>\
<p>Firmware v{firmware}</p>\
<table>\
<tr><th>Uptime</th><td>{uptime} s</td></tr>\
<tr><th>WiFi SSID</th><td>{ssid}</td></tr>\
<tr><th>IPv4</th><td>{ip}</td></tr>\
<tr><th>Cached fobs</th><td>{fobs}</td></tr>\
<tr><th>Pending events</th><td>{events}</td></tr>\
<tr><th>Sync ETag</th><td>{etag}</td></tr>\
</table>\
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

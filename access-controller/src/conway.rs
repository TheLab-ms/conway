//! Conway server client - handles sync operations with the Conway fob API.

use core::fmt::Write as FmtWrite;
use core::sync::atomic::{AtomicBool, Ordering};
use esp_radio::wifi::WifiDevice;
use heapless::String as HString;
use smoltcp::iface::{Interface, SocketHandle, SocketSet};
use smoltcp::socket::tcp::{Socket as TcpSocket, SocketBuffer, State as TcpState};
use smoltcp::time::Instant as SmoltcpInstant;
use smoltcp::wire::{IpAddress, Ipv4Address};

use crate::shared::{AccessEvent, MAX_EVENTS, MAX_FOBS, SHARED};
use crate::storage::{Config, Storage};

// Static buffers with taken flag for safety
// Using static buffers avoids stack overflow - Core 1 stack is only 16KB
static SYNC_BUFFERS_TAKEN: AtomicBool = AtomicBool::new(false);
static mut RX_BUF: [u8; 2048] = [0; 2048];
static mut TX_BUF: [u8; 1024] = [0; 1024];
static mut RESPONSE_BUF: [u8; 2048] = [0; 2048];

/// Guard that releases the buffer flag on drop
struct SyncBufferGuard;

impl Drop for SyncBufferGuard {
    fn drop(&mut self) {
        SYNC_BUFFERS_TAKEN.store(false, Ordering::Release);
    }
}

/// Borrow the sync buffers, panics if already taken (re-entrancy detected)
fn take_sync_buffers() -> (&'static mut [u8], &'static mut [u8], &'static mut [u8], SyncBufferGuard) {
    if SYNC_BUFFERS_TAKEN.swap(true, Ordering::Acquire) {
        panic!("sync_with_conway called reentrantly!");
    }
    // SAFETY: We just set SYNC_BUFFERS_TAKEN to true, so we have exclusive access
    unsafe {
        (&mut RX_BUF[..], &mut TX_BUF[..], &mut RESPONSE_BUF[..], SyncBufferGuard)
    }
}

/// Properly close and remove a socket to avoid leaving TCP connections half-open.
fn close_and_remove_socket(sockets: &mut SocketSet<'_>, handle: SocketHandle) {
    let socket = sockets.get_mut::<TcpSocket>(handle);
    socket.close();
    sockets.remove(handle);
}

/// Sync with Conway server - fetches fob list and reports access events.
/// Events are only removed from the queue after successful server acknowledgment.
pub fn sync_with_conway(
    iface: &mut Interface,
    device: &mut WifiDevice<'_>,
    sockets: &mut SocketSet<'_>,
    config: &Config,
    storage: &mut Storage,
    etag: &mut HString<64>,
) {
    crate::heap_debug::log_heap_stats("sync:entry");

    // Peek at pending events without removing them from the queue.
    // They will only be removed after the server acknowledges receipt.
    let mut events: [AccessEvent; MAX_EVENTS] = [AccessEvent::default(); MAX_EVENTS];
    let (event_count, event_tail) = SHARED.peek_events(&mut events);

    // Parse Conway host IP without heap allocation
    let remote_ip = match parse_ipv4(&config.conway_host) {
        Some(ip) => ip,
        None => {
            log::error!("conway: invalid host IP: {}", config.conway_host);
            return;
        }
    };

    // Take buffers with re-entrancy guard
    let (rx_buf, tx_buf, response_buf, _guard) = take_sync_buffers();
    crate::heap_debug::log_heap_stats("sync:after_take_buffers");
    let rx_buffer = SocketBuffer::new(rx_buf);
    let tx_buffer = SocketBuffer::new(tx_buf);
    let tcp_socket = TcpSocket::new(rx_buffer, tx_buffer);
    let handle = sockets.add(tcp_socket);

    // Connect to server
    {
        let socket = sockets.get_mut::<TcpSocket>(handle);
        let remote = (IpAddress::Ipv4(remote_ip), config.conway_port);
        if socket.connect(iface.context(), remote, 49152).is_err() {
            log::error!("conway: connect initiation failed");
            close_and_remove_socket(sockets, handle);
            return;
        }
    }

    // Poll until connected or timeout
    let deadline = esp_hal::time::Instant::now().duration_since_epoch().as_millis() + 5000;
    let mut last_wdt_feed = esp_hal::time::Instant::now().duration_since_epoch().as_millis();
    loop {
        let now = esp_hal::time::Instant::now().duration_since_epoch().as_millis();
        let smoltcp_now = SmoltcpInstant::from_millis(now as i64);
        iface.poll(smoltcp_now, device, sockets);

        let socket = sockets.get_mut::<TcpSocket>(handle);
        if socket.may_send() {
            break;
        }
        if now > deadline || socket.state() == TcpState::Closed {
            log::error!("conway: connection timeout");
            close_and_remove_socket(sockets, handle);
            return;
        }

        // Feed watchdog during long network waits to prevent reset
        if now - last_wdt_feed >= 5000 {
            last_wdt_feed = now;
            crate::feed_watchdog();
        }

        esp_radio_rtos_driver::usleep(10_000);
    }

    // Build and send HTTP request
    let mut body: HString<512> = HString::new();
    let _ = body.push_str("[");
    for (i, event) in events[..event_count].iter().enumerate() {
        if i > 0 {
            let _ = body.push_str(",");
        }
        let _ = write!(body, r#"{{"fob":{},"allowed":{}}}"#, event.fob, event.allowed);
    }
    let _ = body.push_str("]");

    let mut request: HString<768> = HString::new();
    let _ = write!(
        request,
        "POST /api/fobs HTTP/1.1\r\n\
         Host: {}\r\n\
         Content-Type: application/json\r\n\
         Content-Length: {}\r\n\
         If-None-Match: {}\r\n\
         Connection: close\r\n\r\n{}",
        config.conway_host,
        body.len(),
        etag,
        body
    );

    // Send request
    {
        let socket = sockets.get_mut::<TcpSocket>(handle);
        if socket.send_slice(request.as_bytes()).is_err() {
            log::error!("conway: send failed");
            close_and_remove_socket(sockets, handle);
            return;
        }
    }

    // Poll until response received or timeout
    // response_buf is from the static buffer pool (take_sync_buffers)
    let mut response_len = 0;
    let deadline = esp_hal::time::Instant::now().duration_since_epoch().as_millis() + 5000;
    let mut last_wdt_feed = esp_hal::time::Instant::now().duration_since_epoch().as_millis();

    loop {
        let now = esp_hal::time::Instant::now().duration_since_epoch().as_millis();
        let smoltcp_now = SmoltcpInstant::from_millis(now as i64);
        iface.poll(smoltcp_now, device, sockets);

        let socket = sockets.get_mut::<TcpSocket>(handle);

        // Read any available data
        if socket.may_recv() {
            if let Ok(n) = socket.recv_slice(&mut response_buf[response_len..]) {
                response_len += n;
            }
        }

        // Check if we have complete response (look for \r\n\r\n and Content-Length)
        if response_len > 0 {
            if let Some(header_end) = find_header_end(&response_buf[..response_len]) {
                // Check if we have the full body
                if let Some(content_len) = parse_content_length(&response_buf[..header_end]) {
                    if response_len >= header_end + 4 + content_len {
                        break; // Full response received
                    }
                } else if socket.state() == TcpState::CloseWait
                    || socket.state() == TcpState::Closed
                {
                    break; // Server closed, we have what we'll get
                }
            }
        }

        if now > deadline {
            log::error!("conway: response timeout");
            close_and_remove_socket(sockets, handle);
            return;
        }

        if socket.state() == TcpState::Closed && response_len == 0 {
            log::error!("conway: connection closed unexpectedly");
            close_and_remove_socket(sockets, handle);
            return;
        }

        // Feed watchdog during long network waits to prevent reset
        if now - last_wdt_feed >= 5000 {
            last_wdt_feed = now;
            crate::feed_watchdog();
        }

        esp_radio_rtos_driver::usleep(10_000);
    }

    // Close and remove socket
    close_and_remove_socket(sockets, handle);
    crate::heap_debug::log_heap_stats("sync:after_close_socket");

    // Parse response and commit events only on success
    match parse_conway_response(&response_buf[..response_len], storage, etag) {
        Ok(()) => {
            // Server acknowledged the request (200 or 304) - safe to remove events from queue
            SHARED.commit_events(event_count, event_tail);
        }
        Err(e) => {
            // Request failed - events remain in queue for retry on next sync
            log::error!("conway: {}", e);
        }
    }
}

fn find_header_end(data: &[u8]) -> Option<usize> {
    data.windows(4).position(|w| w == b"\r\n\r\n")
}

/// Parse an IPv4 address from a string without heap allocation.
fn parse_ipv4(s: &str) -> Option<Ipv4Address> {
    let mut octets = [0u8; 4];
    let mut octet_idx = 0;

    for part in s.split('.') {
        if octet_idx >= 4 {
            return None;
        }
        octets[octet_idx] = part.parse().ok()?;
        octet_idx += 1;
    }

    if octet_idx == 4 {
        Some(Ipv4Address::new(octets[0], octets[1], octets[2], octets[3]))
    } else {
        None
    }
}

fn parse_content_length(header: &[u8]) -> Option<usize> {
    let header_str = core::str::from_utf8(header).ok()?;
    for line in header_str.lines() {
        // Case-insensitive prefix check without allocation
        if line.len() >= 15 && line[..15].eq_ignore_ascii_case("content-length:") {
            return line[15..].trim().parse().ok();
        }
    }
    None
}

fn parse_conway_response(
    data: &[u8],
    storage: &mut Storage,
    etag: &mut HString<64>,
) -> Result<(), &'static str> {
    let header_end = find_header_end(data).ok_or("malformed response")?;
    let header = core::str::from_utf8(&data[..header_end]).map_err(|_| "bad header encoding")?;
    let body = &data[header_end + 4..];

    // Parse status line
    let status_line = header.lines().next().ok_or("no status line")?;
    let status: u16 = status_line
        .split_whitespace()
        .nth(1)
        .and_then(|s| s.parse().ok())
        .ok_or("bad status code")?;

    match status {
        304 => {
            log::debug!("conway: not modified");
            Ok(())
        }
        200 => {
            // Extract ETag header (case-insensitive, no allocation)
            for line in header.lines() {
                if line.len() >= 5 && line[..5].eq_ignore_ascii_case("etag:") {
                    etag.clear();
                    let _ = etag.push_str(line[5..].trim());
                }
            }

            // Parse fob list
            let json = core::str::from_utf8(body).map_err(|_| "bad body encoding")?;
            let fobs = parse_fob_list(json)?;

            log::info!("conway: synced {} fobs", fobs.len());

            // Update shared state and persist
            SHARED.update_fobs(&fobs);

            crate::heap_debug::log_heap_stats("conway:before_save_fobs");
            storage.save_fobs(&fobs);
            crate::heap_debug::log_heap_stats("conway:after_save_fobs");

            storage.save_etag(etag);

            Ok(())
        }
        _ => Err("unexpected status"),
    }
}

fn parse_fob_list(json: &str) -> Result<heapless::Vec<u32, MAX_FOBS>, &'static str> {
    let trimmed = json.trim();
    if !trimmed.starts_with('[') || !trimmed.ends_with(']') {
        return Err("not a JSON array");
    }

    let inner = &trimmed[1..trimmed.len() - 1];
    let mut fobs = heapless::Vec::new();

    for part in inner.split(',') {
        let part = part.trim();
        if part.is_empty() {
            continue;
        }
        if let Ok(fob) = part.parse::<u32>() {
            if fobs.push(fob).is_err() {
                log::warn!("conway: fob list truncated at {} entries", MAX_FOBS);
                break;
            }
        }
    }

    Ok(fobs)
}

/// Handle incoming HTTP server requests for admin interface.
pub fn handle_http_server(sockets: &mut SocketSet<'_>, handle: SocketHandle, etag: &str) {
    let socket = sockets.get_mut::<TcpSocket>(handle);

    // Handle socket state machine for immediate reuse
    match socket.state() {
        TcpState::Closed => {
            // Ready to accept new connections
            socket.listen(80).ok();
            return;
        }
        TcpState::CloseWait => {
            // Remote closed, we can close our end immediately
            socket.close();
            return;
        }
        TcpState::TimeWait | TcpState::LastAck | TcpState::Closing => {
            // Abort to skip TIME_WAIT and allow immediate reuse
            // This is acceptable for a simple admin interface
            socket.abort();
            return;
        }
        TcpState::Listen | TcpState::SynReceived | TcpState::SynSent => {
            // Waiting for connection, nothing to do
            return;
        }
        TcpState::Established | TcpState::FinWait1 | TcpState::FinWait2 => {
            // Connected or closing - proceed to handle request if we can receive
        }
    }

    if !socket.may_recv() {
        return;
    }

    // Read request
    let mut buf = [0u8; 512];
    let len = match socket.recv_slice(&mut buf) {
        Ok(n) => n,
        Err(_) => return,
    };

    if len == 0 {
        return;
    }

    let request = match core::str::from_utf8(&buf[..len]) {
        Ok(s) => s,
        Err(_) => return,
    };

    // Parse method and path
    let line = request.lines().next().unwrap_or("");
    let mut parts = line.split_whitespace();
    let method = parts.next().unwrap_or("");
    let path = parts.next().unwrap_or("");

    // Generate response
    let response = match (method, path) {
        ("POST", "/unlock") => {
            SHARED.request_unlock();
            http_response("200 OK", "<p>Door unlocked!</p>")
        }
        ("GET", "/" | "") => {
            let count = SHARED.fob_count.load(Ordering::Relaxed);
            let mut body: HString<256> = HString::new();
            let _ = write!(
                body,
                "<h1>Conway</h1>\
                 <p>ETag: {}</p>\
                 <p>Fobs: {}</p>\
                 <form action=/unlock method=post>\
                 <button>Unlock</button></form>",
                if etag.is_empty() { "(none)" } else { etag },
                count
            );
            http_response("200 OK", &body)
        }
        _ => http_response("404 Not Found", "<p>Not Found</p>"),
    };

    // Send response and initiate close
    socket.send_slice(response.as_bytes()).ok();
    socket.close();
}

fn http_response(status: &str, body: &str) -> HString<512> {
    let mut r: HString<512> = HString::new();
    let _ = write!(
        r,
        "HTTP/1.1 {}\r\n\
         Content-Type: text/html\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\r\n{}",
        status,
        body.len(),
        body
    );
    r
}

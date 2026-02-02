//! Conway API sync using its simple HTTP protocol.
//!
//! Active fob IDs are cached in-memory alongside an etag.
//! The etag is sent to the server every 10 seconds.
//! It will respond with a 304 if the cache is still valid.
//!
//! Each request can include fob swipe events to be stored.
//! A bounded set of events are held in-memory.

use core::fmt::Write as FmtWrite;
use embassy_net::tcp::TcpSocket;
use embassy_net::Stack;
use embassy_sync::blocking_mutex::raw::CriticalSectionRawMutex;
use embassy_sync::mutex::Mutex;
use embassy_time::Duration;
use embedded_io_async::Write;
use heapless::String as HString;
use smoltcp::wire::IpAddress;

use crate::{CONWAY_HOST, CONWAY_PORT, EVENT_BUFFER, MAX_FOBS, SYNC_COMPLETE};

const IO_TIMEOUT: Duration = Duration::from_secs(10);

/// Sync with Conway server using raw TCP HTTP.
/// Events are only removed from the buffer after successful server acknowledgment.
pub async fn sync_with_conway(
    stack: &'static Stack<'static>,
    fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &'static Mutex<CriticalSectionRawMutex, HString<64>>,
) {
    // Peek at pending events without removing them from the buffer.
    // They will only be removed after the server acknowledges receipt.
    let mut events: [AccessEvent; MAX_EVENTS] = [AccessEvent::default(); MAX_EVENTS];
    let (event_count, event_tail) = EVENT_BUFFER.peek(&mut events).await;

    // Build request body with events
    let mut body: HString<512> = HString::new();
    let _ = body.push_str("[");
    for i in 0..event_count {
        if i > 0 {
            let _ = body.push_str(",");
        }
        let _ = write!(
            body,
            r#"{{"fob":{},"allowed":{}}}"#,
            events[i].fob, events[i].allowed
        );
    }
    let _ = body.push_str("]");

    // Get current ETag for If-None-Match header
    let current_etag = {
        let guard = etag.lock().await;
        guard.clone()
    };

    // Parse host as IP address
    let remote_addr = match parse_ipv4(CONWAY_HOST) {
        Some(ip) => IpAddress::Ipv4(ip),
        None => {
            log::error!("sync: invalid IP address: {}", CONWAY_HOST);
            SYNC_COMPLETE.signal(());
            return;
        }
    };

    // Create TCP socket
    let mut rx_buf = [0u8; 2048];
    let mut tx_buf = [0u8; 1024];
    let mut socket = TcpSocket::new(*stack, &mut rx_buf, &mut tx_buf);
    socket.set_timeout(Some(IO_TIMEOUT));

    // Connect to server
    let remote = smoltcp::wire::IpEndpoint::new(remote_addr, CONWAY_PORT);
    log::debug!("sync: connecting to {:?}", remote);

    if let Err(e) = socket.connect(remote).await {
        log::error!("sync: connect failed: {:?}", e);
        socket.abort();
        SYNC_COMPLETE.signal(());
        return;
    }

    // Build and send HTTP request
    let mut request: HString<512> = HString::new();
    let _ = write!(
        request,
        "POST /api/fobs HTTP/1.1\r\n\
         Host: {}\r\n\
         Content-Type: application/json\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n",
        CONWAY_HOST,
        body.len()
    );
    if !current_etag.is_empty() {
        let _ = write!(request, "If-None-Match: {}\r\n", current_etag);
    }
    let _ = request.push_str("\r\n");

    // Send request headers
    if let Err(e) = socket.write_all(request.as_bytes()).await {
        log::error!("sync: write headers failed: {:?}", e);
        socket.abort();
        SYNC_COMPLETE.signal(());
        return;
    }

    // Send request body
    if let Err(e) = socket.write_all(body.as_bytes()).await {
        log::error!("sync: write body failed: {:?}", e);
        socket.abort();
        SYNC_COMPLETE.signal(());
        return;
    }

    // Read response
    let mut response_buf = [0u8; 2048];
    let mut total_read = 0;

    loop {
        match socket.read(&mut response_buf[total_read..]).await {
            Ok(0) => break, // Connection closed
            Ok(n) => {
                total_read += n;
                if total_read >= response_buf.len() {
                    break;
                }
            }
            Err(e) => {
                log::error!("sync: read failed: {:?}", e);
                socket.abort();
                SYNC_COMPLETE.signal(());
                return;
            }
        }
    }

    socket.abort();

    // Parse HTTP response
    let response = match core::str::from_utf8(&response_buf[..total_read]) {
        Ok(s) => s,
        Err(_) => {
            log::error!("sync: invalid response encoding");
            SYNC_COMPLETE.signal(());
            return;
        }
    };

    // Parse status code
    let status = parse_status_code(response);
    log::debug!("sync: status {}", status);

    match status {
        304 => {
            log::debug!("sync: not modified");
            // Server acknowledged the request - safe to remove events from buffer
            EVENT_BUFFER.commit(event_count, event_tail).await;
        }
        200 => {
            // Extract ETag from headers
            let new_etag = extract_header(response, "etag");

            // Find body (after \r\n\r\n)
            let body_start = response.find("\r\n\r\n").map(|i| i + 4);
            let response_body = body_start.map(|i| &response[i..]).unwrap_or("");

            // Parse fob list
            let new_fobs = match parse_fob_list(response_body) {
                Ok(f) => f,
                Err(e) => {
                    log::error!("sync: {}", e);
                    // Don't commit events - they will be retried on next sync
                    SYNC_COMPLETE.signal(());
                    return;
                }
            };

            log::info!("sync: received {} fobs", new_fobs.len());

            // Update shared fob list
            {
                let mut guard = fobs.lock().await;
                guard.clear();
                for &f in new_fobs.iter() {
                    let _ = guard.push(f);
                }
            }

            // Update etag
            if let Some(etag_value) = new_etag {
                let mut guard = etag.lock().await;
                guard.clear();
                let _ = guard.push_str(etag_value);
            }

            // Server acknowledged the request - safe to remove events from buffer
            EVENT_BUFFER.commit(event_count, event_tail).await;
        }
        _ => {
            log::error!("sync: unexpected status: {}", status);
            // Don't commit events - they will be retried on next sync
        }
    }

    // Signal that sync is complete (success or failure)
    SYNC_COMPLETE.signal(());
}

/// Parse HTTP status code from response.
fn parse_status_code(response: &str) -> u16 {
    // Format: "HTTP/1.1 200 OK\r\n..."
    response
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|code| code.parse().ok())
        .unwrap_or(0)
}

/// Extract header value (case-insensitive).
fn extract_header<'a>(response: &'a str, name: &str) -> Option<&'a str> {
    for line in response.lines() {
        if line.is_empty() || line == "\r" {
            break; // End of headers
        }
        if let Some((key, value)) = line.split_once(':') {
            if key.trim().eq_ignore_ascii_case(name) {
                return Some(value.trim());
            }
        }
    }
    None
}

/// Parse IPv4 address string.
fn parse_ipv4(s: &str) -> Option<smoltcp::wire::Ipv4Address> {
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
        Some(smoltcp::wire::Ipv4Address::new(
            octets[0], octets[1], octets[2], octets[3],
        ))
    } else {
        None
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
                log::warn!("sync: fob list truncated at {}", MAX_FOBS);
                break;
            }
        }
    }

    Ok(fobs)
}

pub const MAX_EVENTS: usize = 20;

/// Access event to report to Conway server.
#[derive(Clone, Copy, Default)]
pub struct AccessEvent {
    pub fob: u32,
    pub allowed: bool,
}

/// Event buffer state.
struct EventBufferInner {
    events: [AccessEvent; MAX_EVENTS],
    head: usize, // next write position
    tail: usize, // next read position
}

impl EventBufferInner {
    const fn new() -> Self {
        Self {
            events: [AccessEvent { fob: 0, allowed: false }; MAX_EVENTS],
            head: 0,
            tail: 0,
        }
    }

    fn len(&self) -> usize {
        if self.head >= self.tail {
            self.head - self.tail
        } else {
            MAX_EVENTS - self.tail + self.head
        }
    }

    fn is_full(&self) -> bool {
        (self.head + 1) % MAX_EVENTS == self.tail
    }
}

/// Thread-safe event buffer with peek/commit semantics.
pub struct EventBuffer {
    inner: Mutex<CriticalSectionRawMutex, EventBufferInner>,
}

impl EventBuffer {
    pub const fn new() -> Self {
        Self {
            inner: Mutex::new(EventBufferInner::new()),
        }
    }

    /// Push an event to the buffer.
    /// If the buffer is full, the oldest event is discarded.
    pub async fn push(&self, event: AccessEvent) {
        let mut guard = self.inner.lock().await;

        // If buffer is full, advance tail to discard oldest event
        if guard.is_full() {
            log::warn!("events: buffer full, dropping oldest event");
            guard.tail = (guard.tail + 1) % MAX_EVENTS;
        }

        let head = guard.head;
        guard.events[head] = event;
        guard.head = (head + 1) % MAX_EVENTS;
    }

    /// Peek at pending events without removing them.
    /// Returns (events, count, tail_snapshot).
    /// The tail_snapshot should be passed to commit() after successful sync.
    pub async fn peek(&self, out: &mut [AccessEvent; MAX_EVENTS]) -> (usize, usize) {
        let guard = self.inner.lock().await;
        let tail = guard.tail;
        let head = guard.head;

        let mut count = 0;
        let mut idx = tail;
        while idx != head && count < MAX_EVENTS {
            out[count] = guard.events[idx];
            count += 1;
            idx = (idx + 1) % MAX_EVENTS;
        }

        (count, tail)
    }

    /// Commit (remove) events from the buffer after successful transmission.
    /// Takes the tail_snapshot from peek(). If tail has changed (buffer overflow
    /// occurred during sync), this adjusts accordingly.
    pub async fn commit(&self, count: usize, expected_tail: usize) {
        let mut guard = self.inner.lock().await;

        // Calculate where tail should be after committing
        let new_tail = (expected_tail + count) % MAX_EVENTS;

        // Only update if tail hasn't been modified by overflow handling
        if guard.tail == expected_tail {
            guard.tail = new_tail;
            log::debug!("events: committed {} events", count);
        } else {
            // Tail was moved by overflow - only advance if we would move it forward
            // Calculate the distance from current tail to new_tail in circular space
            let distance_forward = if new_tail >= guard.tail {
                new_tail - guard.tail
            } else {
                MAX_EVENTS - guard.tail + new_tail
            };

            // If new_tail is ahead of current tail in circular space, advance to it
            // Otherwise, overflow already moved tail past where we would commit to
            if distance_forward < MAX_EVENTS / 2 {
                // new_tail is ahead - advance tail
                guard.tail = new_tail;
                log::debug!(
                    "events: committed {} events (adjusted after overflow moved tail from {} to {})",
                    count,
                    expected_tail,
                    new_tail
                );
            } else {
                // new_tail is behind or equal - overflow already discarded our events
                log::debug!(
                    "events: peeked events already removed by overflow (tail moved from {} to {}, would commit to {})",
                    expected_tail,
                    guard.tail,
                    new_tail
                );
            }
        }
    }

    /// Get current event count (for status display).
    pub async fn len(&self) -> usize {
        let guard = self.inner.lock().await;
        guard.len()
    }
}

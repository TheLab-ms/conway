//! Simple HTTP server using raw TCP sockets.

use core::fmt::Write as FmtWrite;
use embassy_net::tcp::TcpSocket;
use embassy_net::Stack;
use embassy_sync::blocking_mutex::raw::CriticalSectionRawMutex;
use embassy_sync::mutex::Mutex;
use embassy_time::Duration;
use embedded_io_async::Write;
use heapless::String as HString;

use crate::storage::MAX_FOBS;
use crate::DOOR_SIGNAL;

const IO_TIMEOUT: Duration = Duration::from_secs(10);

/// Run the HTTP server task.
pub async fn run_server(
    stack: &'static Stack<'static>,
    fobs: &'static Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &'static Mutex<CriticalSectionRawMutex, HString<64>>,
) {
    let mut rx_buf = [0u8; 1024];
    let mut tx_buf = [0u8; 1024];

    loop {
        let mut socket = TcpSocket::new(*stack, &mut rx_buf, &mut tx_buf);
        socket.set_timeout(Some(IO_TIMEOUT));

        if socket.accept(80).await.is_err() {
            socket.abort();
            continue;
        }

        handle_request(&mut socket, fobs, etag).await;
        socket.abort();
    }
}

async fn handle_request(
    socket: &mut TcpSocket<'_>,
    fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &Mutex<CriticalSectionRawMutex, HString<64>>,
) {
    // Read request
    let mut request_buf = [0u8; 512];
    let n = match socket.read(&mut request_buf).await {
        Ok(n) if n > 0 => n,
        _ => return,
    };

    let request = match core::str::from_utf8(&request_buf[..n]) {
        Ok(s) => s,
        Err(_) => return,
    };

    // Parse request line
    let first_line = request.lines().next().unwrap_or("");
    let mut parts = first_line.split_whitespace();
    let method = parts.next().unwrap_or("");
    let path = parts.next().unwrap_or("").split('?').next().unwrap_or("");

    // Route request
    match (method, path) {
        ("GET", "/") => handle_index(socket, fobs, etag).await,
        ("POST", "/unlock") => handle_unlock(socket).await,
        _ => send_response(socket, 404, "Not Found", "text/plain", "Not Found").await,
    }
}

async fn handle_index(
    socket: &mut TcpSocket<'_>,
    fobs: &Mutex<CriticalSectionRawMutex, heapless::Vec<u32, MAX_FOBS>>,
    etag: &Mutex<CriticalSectionRawMutex, HString<64>>,
) {
    let count = fobs.lock().await.len();
    let current_etag = {
        let guard = etag.lock().await;
        guard.clone()
    };

    let mut body: HString<512> = HString::new();
    let _ = write!(
        body,
        "<h1>Conway</h1>\
         <p>ETag: {}</p>\
         <p>Fobs: {}</p>\
         <form action=/unlock method=post>\
         <button>Unlock</button></form>",
        if current_etag.is_empty() {
            "(none)"
        } else {
            current_etag.as_str()
        },
        count
    );

    send_response(socket, 200, "OK", "text/html", body.as_str()).await;
}

async fn handle_unlock(socket: &mut TcpSocket<'_>) {
    DOOR_SIGNAL.signal(());
    send_response(socket, 200, "OK", "text/html", "<p>Door unlocked!</p>").await;
}

async fn send_response(
    socket: &mut TcpSocket<'_>,
    status: u16,
    status_text: &str,
    content_type: &str,
    body: &str,
) {
    let mut response: HString<1024> = HString::new();
    let _ = write!(
        response,
        "HTTP/1.1 {} {}\r\n\
         Content-Type: {}\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n\
         {}",
        status,
        status_text,
        content_type,
        body.len(),
        body
    );

    let _ = socket.write_all(response.as_bytes()).await;
}

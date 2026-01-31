//! Unit tests for HTTP/sync parsing logic.
//!
//! Tests the parsing functions from sync.rs without requiring network operations.

const MAX_FOBS: usize = 512;

/// Parse HTTP status code from response (mirrors sync.rs).
fn parse_status_code(response: &str) -> u16 {
    // Format: "HTTP/1.1 200 OK\r\n..."
    response
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|code| code.parse().ok())
        .unwrap_or(0)
}

/// Extract header value (case-insensitive) (mirrors sync.rs).
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

/// Parse IPv4 address string (mirrors sync.rs).
fn parse_ipv4(s: &str) -> Option<[u8; 4]> {
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
        Some(octets)
    } else {
        None
    }
}

/// Parse fob list from JSON array (mirrors sync.rs).
fn parse_fob_list(json: &str) -> Result<Vec<u32>, &'static str> {
    let trimmed = json.trim();
    if !trimmed.starts_with('[') || !trimmed.ends_with(']') {
        return Err("not a JSON array");
    }

    let inner = &trimmed[1..trimmed.len() - 1];
    let mut fobs = Vec::new();

    for part in inner.split(',') {
        let part = part.trim();
        if part.is_empty() {
            continue;
        }
        if let Ok(fob) = part.parse::<u32>() {
            if fobs.len() >= MAX_FOBS {
                // In the original, this logs a warning and breaks
                break;
            }
            fobs.push(fob);
        }
    }

    Ok(fobs)
}

// ============================================================================
// Tests for parse_status_code
// ============================================================================

#[test]
fn test_parse_status_code_200() {
    let response = "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n";
    assert_eq!(parse_status_code(response), 200);
}

#[test]
fn test_parse_status_code_304() {
    let response = "HTTP/1.1 304 Not Modified\r\n\r\n";
    assert_eq!(parse_status_code(response), 304);
}

#[test]
fn test_parse_status_code_404() {
    let response = "HTTP/1.1 404 Not Found\r\nContent-Type: text/plain\r\n\r\n";
    assert_eq!(parse_status_code(response), 404);
}

#[test]
fn test_parse_status_code_500() {
    let response = "HTTP/1.1 500 Internal Server Error\r\n\r\n";
    assert_eq!(parse_status_code(response), 500);
}

#[test]
fn test_parse_status_code_http10() {
    let response = "HTTP/1.0 200 OK\r\n\r\n";
    assert_eq!(parse_status_code(response), 200);
}

#[test]
fn test_parse_status_code_empty() {
    assert_eq!(parse_status_code(""), 0);
}

#[test]
fn test_parse_status_code_malformed() {
    // No status code
    assert_eq!(parse_status_code("HTTP/1.1\r\n"), 0);

    // Invalid status code
    assert_eq!(parse_status_code("HTTP/1.1 ABC OK\r\n"), 0);

    // Just garbage
    assert_eq!(parse_status_code("garbage"), 0);
}

#[test]
fn test_parse_status_code_no_reason_phrase() {
    // Some servers omit the reason phrase
    let response = "HTTP/1.1 200\r\n\r\n";
    assert_eq!(parse_status_code(response), 200);
}

#[test]
fn test_parse_status_code_extra_spaces() {
    let response = "HTTP/1.1  200  OK\r\n\r\n";
    assert_eq!(parse_status_code(response), 200);
}

#[test]
fn test_parse_status_code_with_body() {
    let response = "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello";
    assert_eq!(parse_status_code(response), 200);
}

// ============================================================================
// Tests for extract_header
// ============================================================================

#[test]
fn test_extract_header_simple() {
    let response = "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n";
    assert_eq!(extract_header(response, "Content-Type"), Some("text/html"));
}

#[test]
fn test_extract_header_case_insensitive() {
    let response = "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n";

    assert_eq!(extract_header(response, "content-type"), Some("text/html"));
    assert_eq!(extract_header(response, "CONTENT-TYPE"), Some("text/html"));
    assert_eq!(extract_header(response, "Content-type"), Some("text/html"));
}

#[test]
fn test_extract_header_etag() {
    let response = "HTTP/1.1 200 OK\r\nETag: \"abc123\"\r\n\r\n";
    assert_eq!(extract_header(response, "etag"), Some("\"abc123\""));
    assert_eq!(extract_header(response, "ETag"), Some("\"abc123\""));
}

#[test]
fn test_extract_header_with_spaces() {
    let response = "HTTP/1.1 200 OK\r\nX-Custom:   value with spaces   \r\n\r\n";
    assert_eq!(
        extract_header(response, "X-Custom"),
        Some("value with spaces")
    );
}

#[test]
fn test_extract_header_multiple_headers() {
    let response =
        "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: 100\r\nETag: \"xyz\"\r\n\r\n";

    assert_eq!(extract_header(response, "Content-Type"), Some("text/html"));
    assert_eq!(extract_header(response, "Content-Length"), Some("100"));
    assert_eq!(extract_header(response, "ETag"), Some("\"xyz\""));
}

#[test]
fn test_extract_header_not_found() {
    let response = "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n";
    assert_eq!(extract_header(response, "X-Not-Present"), None);
}

#[test]
fn test_extract_header_empty_value() {
    let response = "HTTP/1.1 200 OK\r\nX-Empty:\r\n\r\n";
    assert_eq!(extract_header(response, "X-Empty"), Some(""));
}

#[test]
fn test_extract_header_stops_at_body() {
    // Should not find headers in the body
    let response = "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\nX-In-Body: value\r\n";
    assert_eq!(extract_header(response, "X-In-Body"), None);
}

#[test]
fn test_extract_header_colon_in_value() {
    let response = "HTTP/1.1 200 OK\r\nLocation: http://example.com:8080/path\r\n\r\n";
    assert_eq!(
        extract_header(response, "Location"),
        Some("http://example.com:8080/path")
    );
}

#[test]
fn test_extract_header_no_headers() {
    let response = "HTTP/1.1 200 OK\r\n\r\n";
    assert_eq!(extract_header(response, "Content-Type"), None);
}

// ============================================================================
// Tests for parse_ipv4
// ============================================================================

#[test]
fn test_parse_ipv4_localhost() {
    assert_eq!(parse_ipv4("127.0.0.1"), Some([127, 0, 0, 1]));
}

#[test]
fn test_parse_ipv4_standard() {
    assert_eq!(parse_ipv4("192.168.1.100"), Some([192, 168, 1, 100]));
}

#[test]
fn test_parse_ipv4_zeros() {
    assert_eq!(parse_ipv4("0.0.0.0"), Some([0, 0, 0, 0]));
}

#[test]
fn test_parse_ipv4_max() {
    assert_eq!(parse_ipv4("255.255.255.255"), Some([255, 255, 255, 255]));
}

#[test]
fn test_parse_ipv4_too_few_octets() {
    assert_eq!(parse_ipv4("192.168.1"), None);
    assert_eq!(parse_ipv4("192.168"), None);
    assert_eq!(parse_ipv4("192"), None);
    assert_eq!(parse_ipv4(""), None);
}

#[test]
fn test_parse_ipv4_too_many_octets() {
    assert_eq!(parse_ipv4("192.168.1.1.1"), None);
}

#[test]
fn test_parse_ipv4_invalid_octet() {
    assert_eq!(parse_ipv4("192.168.1.256"), None); // 256 > 255
    assert_eq!(parse_ipv4("192.168.1.abc"), None);
    assert_eq!(parse_ipv4("192.168.1.-1"), None);
}

#[test]
fn test_parse_ipv4_leading_zeros() {
    // Leading zeros should parse as decimal, not octal
    assert_eq!(parse_ipv4("192.168.001.001"), Some([192, 168, 1, 1]));
}

#[test]
fn test_parse_ipv4_empty_octet() {
    assert_eq!(parse_ipv4("192..1.1"), None);
    assert_eq!(parse_ipv4(".168.1.1"), None);
    assert_eq!(parse_ipv4("192.168.1."), None);
}

#[test]
fn test_parse_ipv4_whitespace() {
    // Whitespace should cause parse failure
    assert_eq!(parse_ipv4(" 192.168.1.1"), None);
    assert_eq!(parse_ipv4("192.168.1.1 "), None);
    assert_eq!(parse_ipv4("192. 168.1.1"), None);
}

// ============================================================================
// Tests for parse_fob_list
// ============================================================================

#[test]
fn test_parse_fob_list_empty() {
    assert_eq!(parse_fob_list("[]"), Ok(vec![]));
}

#[test]
fn test_parse_fob_list_single() {
    assert_eq!(parse_fob_list("[12345]"), Ok(vec![12345]));
}

#[test]
fn test_parse_fob_list_multiple() {
    assert_eq!(
        parse_fob_list("[100, 200, 300]"),
        Ok(vec![100, 200, 300])
    );
}

#[test]
fn test_parse_fob_list_no_spaces() {
    assert_eq!(parse_fob_list("[1,2,3,4,5]"), Ok(vec![1, 2, 3, 4, 5]));
}

#[test]
fn test_parse_fob_list_with_whitespace() {
    assert_eq!(
        parse_fob_list("  [  100  ,  200  ,  300  ]  "),
        Ok(vec![100, 200, 300])
    );
}

#[test]
fn test_parse_fob_list_newlines() {
    let json = "[\n  100,\n  200,\n  300\n]";
    assert_eq!(parse_fob_list(json), Ok(vec![100, 200, 300]));
}

#[test]
fn test_parse_fob_list_large_numbers() {
    assert_eq!(
        parse_fob_list("[0, 4294967295]"),
        Ok(vec![0, u32::MAX])
    );
}

#[test]
fn test_parse_fob_list_not_array_object() {
    assert_eq!(parse_fob_list("{}"), Err("not a JSON array"));
}

#[test]
fn test_parse_fob_list_not_array_string() {
    assert_eq!(parse_fob_list("\"hello\""), Err("not a JSON array"));
}

#[test]
fn test_parse_fob_list_not_array_number() {
    assert_eq!(parse_fob_list("123"), Err("not a JSON array"));
}

#[test]
fn test_parse_fob_list_malformed_missing_bracket() {
    assert_eq!(parse_fob_list("[1, 2, 3"), Err("not a JSON array"));
    assert_eq!(parse_fob_list("1, 2, 3]"), Err("not a JSON array"));
}

#[test]
fn test_parse_fob_list_ignores_non_numbers() {
    // Non-numeric values are silently skipped
    assert_eq!(
        parse_fob_list("[100, \"skip\", 200, null, 300]"),
        Ok(vec![100, 200, 300])
    );
}

#[test]
fn test_parse_fob_list_trailing_comma() {
    // Trailing comma creates an empty part which is skipped
    assert_eq!(parse_fob_list("[100, 200, 300,]"), Ok(vec![100, 200, 300]));
}

#[test]
fn test_parse_fob_list_empty_parts() {
    // Multiple commas create empty parts
    assert_eq!(parse_fob_list("[100,,200]"), Ok(vec![100, 200]));
}

#[test]
fn test_parse_fob_list_max_capacity() {
    // Test that the function respects MAX_FOBS limit
    let fobs: Vec<String> = (0..MAX_FOBS + 10).map(|i| i.to_string()).collect();
    let json = format!("[{}]", fobs.join(","));
    let result = parse_fob_list(&json).unwrap();
    assert_eq!(result.len(), MAX_FOBS);
}

#[test]
fn test_parse_fob_list_real_server_response() {
    // Simulate a realistic server response body
    let json = "[10012345, 10098765, 25500001, 4509876]";
    let result = parse_fob_list(json).unwrap();
    assert_eq!(result, vec![10012345, 10098765, 25500001, 4509876]);
}

// ============================================================================
// Integration tests combining parsing functions
// ============================================================================

#[test]
fn test_full_http_response_parsing() {
    let response = "HTTP/1.1 200 OK\r\n\
                    Content-Type: application/json\r\n\
                    ETag: \"v1-abc123\"\r\n\
                    Content-Length: 25\r\n\
                    \r\n\
                    [10012345, 10098765, 100]";

    // Parse status
    assert_eq!(parse_status_code(response), 200);

    // Extract headers
    assert_eq!(
        extract_header(response, "Content-Type"),
        Some("application/json")
    );
    assert_eq!(extract_header(response, "ETag"), Some("\"v1-abc123\""));
    assert_eq!(extract_header(response, "Content-Length"), Some("25"));

    // Extract and parse body
    let body_start = response.find("\r\n\r\n").map(|i| i + 4).unwrap();
    let body = &response[body_start..];
    let fobs = parse_fob_list(body).unwrap();
    assert_eq!(fobs, vec![10012345, 10098765, 100]);
}

#[test]
fn test_304_response_parsing() {
    let response = "HTTP/1.1 304 Not Modified\r\n\
                    ETag: \"v1-abc123\"\r\n\
                    \r\n";

    assert_eq!(parse_status_code(response), 304);
    assert_eq!(extract_header(response, "ETag"), Some("\"v1-abc123\""));

    // 304 has no body
    let body_start = response.find("\r\n\r\n").map(|i| i + 4).unwrap();
    let body = &response[body_start..];
    assert!(body.is_empty() || body.trim().is_empty());
}

#[test]
fn test_error_response_parsing() {
    let response = "HTTP/1.1 500 Internal Server Error\r\n\
                    Content-Type: text/plain\r\n\
                    \r\n\
                    Something went wrong";

    assert_eq!(parse_status_code(response), 500);
    assert_eq!(extract_header(response, "Content-Type"), Some("text/plain"));
}

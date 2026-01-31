//! Heap debugging utilities for diagnosing allocation failures.

/// Log current heap statistics.
/// Call this before large allocations to diagnose OOM panics.
pub fn log_heap_stats(context: &str) {
    // esp_alloc 0.9 uses HEAP.free() and HEAP.used() methods
    let free = esp_alloc::HEAP.free();
    let used = esp_alloc::HEAP.used();
    let total = free + used;

    log::info!(
        "heap[{}]: used={}KB free={}KB (total={}KB)",
        context,
        used / 1024,
        free / 1024,
        total / 1024,
    );
}

/// Check if a contiguous allocation of `size` bytes is likely to succeed.
/// Note: This checks total free space, not contiguous - fragmentation may still cause failure.
pub fn can_allocate(size: usize) -> bool {
    let free = esp_alloc::HEAP.free();
    // Be conservative - require some headroom for allocator metadata
    free > size + 1024
}

/// Log a warning if heap is getting low.
pub fn warn_if_low(threshold_kb: usize, context: &str) {
    let free = esp_alloc::HEAP.free();
    let free_kb = free / 1024;
    if free_kb < threshold_kb {
        log::warn!(
            "heap[{}]: LOW MEMORY - only {}KB free (threshold={}KB)",
            context,
            free_kb,
            threshold_kb
        );
    }
}

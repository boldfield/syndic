// Package transport abstracts the AI participant endpoints and implements the probe, complete, cancel, and usage contract.
//
// Contract invariants (enforced by adapters):
//   - Adapters never compute candidate hashes.
//   - Secrets the adapter inserts are resolved in memory for the duration of one request,
//     never persisted anywhere, and dropped when the request completes.
//   - Secrets a server echoes are scrubbed from raw output before persistence by exact-match
//     substitution with a marker, and the scrub count is recorded.
//   - Adapters never follow redirects to a different host, never substitute endpoints,
//     and never change billing mode or route to recover from errors.
//   - Adapters never retry a failed request and never change endpoints to recover.
//   - Adapters never execute a returned tool call.
//   - Raw output is preserved before any parsing, with exactly one transformation:
//     the secret scrub. The journal stores scrubbed output with the scrub count.
//
// Invariant this package cannot enforce:
//   - A server may echo sensitive content other than the resolved secrets. That content
//     is treated as potentially sensitive by the system: the raw output is local-only,
//     never exported or committed in an unredacted form.
package transport

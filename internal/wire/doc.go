// Package wire implements the control socket protocol v1 frame types and codec.
//
// The protocol uses newline-delimited JSON for frames. Each frame is encoded as a JSON object
// with a maximum size of 1 MiB including the newline. Frames larger than this limit cannot
// be written or read.
//
// Content exceeding a frame limit (message bodies, attempt output) is never carried inline
// in an event. Instead, it is fetched via the content request type, which pages content in
// 256 KiB chunks with SHA-256 verification.
package wire

// Package wrapper implements the client-side QUIC “wrapper”.
//
// Design goal (current mainline): QUIC-transparent migration.
//
// In classic reconnect-based migration, the client receives a "migrate" control
// message and dials a new server address/port, rebuilding the QUIC session.
// That is fast (especially with 0-RTT), but QUIC is *not* transparent.
//
// In this project we primarily run in *transparent mode*:
//   - The QUIC client always connects to a stable endpoint (typically a UDP proxy).
//   - The server instance can move (A -> B) and rebind UDP after CRIU restore.
//   - The proxy updates its backend destination and forwards UDP packets.
//   - From the client's perspective, the QUIC connection target stays the same.
//
// Responsibilities of this package:
//   - Dial a QUIC connection to Target.
//   - Open the first bidirectional stream as a control stream (newline JSON).
//   - Watch for "migrate" messages and expose MigrateSeen to the application.
//   - Keep the API minimal: the application owns business streams and IO.
//
// Notes on quic-go APIs used here:
//   - quic.DialAddr / quic.DialAddrEarly: establish a QUIC session over UDP.
//   - Connection.OpenStreamSync: open a bidirectional stream.
//   - Stream deadlines (SetReadDeadline/SetWriteDeadline) are used by the APP
//     to bound “business-level” stall time during migration.
package wrapper

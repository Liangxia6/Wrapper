// Package wrapper implements the server-side QUIC wrapper that runs *inside* the container.
//
// High-level flow:
//   - Listen on UDP (inside container), then create a QUIC listener on top.
//   - The first stream of each connection is a control stream.
//   - Remaining streams are business streams handled by the APP (echo / etc).
//
// Migration integration:
//   - Control process (outside containers) sends SIGTERM to trigger broadcasting
//     a "migrate" message to clients and waiting for ACK.
//   - After CRIU restore into container B, control sends SIGUSR2 to trigger UDP rebind.
//     This is required because the restored process must create a fresh UDP socket
//     that matches the new network namespace / port mapping.
//
// Key type: MigratableUDP
//   - Implements net.PacketConn-like behavior and supports Rebind() without killing
//     the QUIC listener. This is critical: quic-go is concurrently reading from
//     the UDP socket, so we must swap sockets carefully.
package wrapper

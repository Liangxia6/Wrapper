package wrapper

import "crypto/tls"

const DefaultALPN = "wrapper-quic/1"

// Shared TLS session cache enables session resumption and 0-RTT on reconnect.
// Without this, quic-go can't perform 0-RTT, even if DialAddrEarly is used.
var clientSessionCache = tls.NewLRUClientSessionCache(256)

var baseClientTLSConfig = &tls.Config{
	InsecureSkipVerify: true,
	NextProtos:         []string{DefaultALPN},
	ClientSessionCache: clientSessionCache,
	MinVersion:         tls.VersionTLS13,
}

func ClientTLSConfig() *tls.Config {
	// Return a clone so callers can safely tweak fields (if needed) without
	// racing; the session cache remains shared.
	return baseClientTLSConfig.Clone()
}

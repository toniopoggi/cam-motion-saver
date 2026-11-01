# CamFreeNest JmDNS compatibility patch

Vendored Java sources from org.jmdns:jmdns:3.6.3, Apache License 2.0.
Upstream: https://github.com/jmdns/jmdns/tree/v3.6.3

The SocketListener query-routing condition includes a non-5353 source port
as well as the QU bit. This restores legacy one-shot unicast replies required
by RFC 6762 sections 5.1 and 6.7 and used by Android system .local resolution.
The remaining upstream Java source is unchanged. The local module is built
from source, so no external patched binary is necessary.

AndroidQueryTest exercises actual UDP requests and replies on loopback. It
simulates successful outgoing multicast probes because the build environment
has no multicast route. It does not claim to test a Wi-Fi multicast path.
The regression fails against unpatched JmDNS 3.6.3, while the existing QU case
passes. After the patch, both cases return answers to the requesting socket.

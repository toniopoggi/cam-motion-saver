package javax.jmdns.impl;

import java.io.IOException;
import java.net.InetAddress;
import java.util.concurrent.atomic.AtomicLong;

/** CamFreeNest diagnostics around the production responder; no pairing secrets. */
public final class ViewerMdns extends JmDNSImpl {
    private final AtomicLong hostQueries = new AtomicLong();
    private final AtomicLong unicastReplies = new AtomicLong();
    private volatile String lastReplyTarget = "none";
    private volatile String lastSendError = "none";

    public ViewerMdns(InetAddress address, String name) throws IOException { super(address, name, 0); }

    @Override public void handleQuery(DNSIncoming message, InetAddress address, int port) throws IOException {
        if (isAnnounced() && message.getQuestions().stream().anyMatch(q -> q.getName().equalsIgnoreCase(getHostName()))) {
            hostQueries.incrementAndGet();
            lastReplyTarget = address.getHostAddress() + ":" + port;
        }
        super.handleQuery(message, address, port);
    }

    @Override public void send(DNSOutgoing message) throws IOException {
        try {
            super.send(message);
            if (unicastReplies != null && message.isResponse() && message.getDestination() != null
                    && !message.getDestination().getAddress().isMulticastAddress()) unicastReplies.incrementAndGet();
        } catch (IOException e) {
            lastSendError = String.valueOf(e.getMessage());
            throw e;
        }
    }
    public String getLastSendError() { return lastSendError; }
    public String diagnostics() {
        return "Host: " + getHostName() + "\nAnnounced: " + isAnnounced()
            + "\nHostname queries received after announcement: " + hostQueries.get()
            + "\nUnicast replies sent: " + unicastReplies.get()
            + "\nLast hostname reply target: " + lastReplyTarget + "\nLast send error: " + lastSendError;
    }
}

package javax.jmdns.impl;

import org.junit.Test;
import static org.junit.Assert.*;
import java.io.*;
import java.net.*;

public class AndroidQueryTest {
    // Only multicast transmission is simulated. The production SocketListener,
    // query dispatcher, responder, DNS encoder and UDP unicast I/O all run.
    static class Responder extends JmDNSImpl {
        Responder() throws IOException {
            super(InetAddress.getByAddress("camfreenest-test", new byte[]{127,0,0,1}), "camfreenest-test", 0);
        }
        @Override public void send(DNSOutgoing out) throws IOException {
            if (out.getDestination() != null && !out.getDestination().getAddress().isMulticastAddress()) super.send(out);
        }
    }
    @Test public void androidOneShotAndQUQueriesReturnToRequestingSocket() throws Exception {
        try (Responder dns = new Responder()) {
            assertTrue("Responder failed to announce", dns.waitForAnnounced(10000));
            for (int queryClass : new int[]{0x8001, 1}) {
                try (DatagramSocket client = new DatagramSocket(new InetSocketAddress("127.0.0.1", 0))) {
                    client.setSoTimeout(1500);
                    ByteArrayOutputStream bytes = new ByteArrayOutputStream();
                    DataOutputStream query = new DataOutputStream(bytes);
                    query.writeShort(0x1234); query.writeShort(0); query.writeShort(1);
                    query.writeShort(0); query.writeShort(0); query.writeShort(0);
                    for (String label : "camfreenest-test.local".split("\\.")) { query.writeByte(label.length()); query.writeBytes(label); }
                    query.writeByte(0); query.writeShort(1); query.writeShort(queryClass);
                    byte[] message = bytes.toByteArray();
                    client.send(new DatagramPacket(message, message.length, InetAddress.getByName("127.0.0.1"), 5353));
                    DatagramPacket reply = new DatagramPacket(new byte[4096], 4096);
                    client.receive(reply);
                    assertEquals(5353, reply.getPort());
                    DNSIncoming answer = new DNSIncoming(reply);
                    assertTrue(answer.isResponse());
                    assertEquals(0x1234, new DataInputStream(new ByteArrayInputStream(reply.getData(), reply.getOffset(), reply.getLength())).readUnsignedShort());
                    assertFalse(answer.getAnswers().isEmpty());
                    assertTrue(answer.getAnswers().stream().anyMatch(record -> record.getName().equals("camfreenest-test.local.")
                        && record instanceof DNSRecord.IPv4Address));
                    // Legacy replies include the original question and no cache-flush bit.
                    assertEquals(1, answer.getQuestions().size());
                    assertFalse(answer.getAnswers().iterator().next().isUnique());
                }
            }
        }
    }
}

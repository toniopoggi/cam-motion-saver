package org.camfreenest.android

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.SystemClock
import engine.Engine
import java.io.Closeable
import java.io.IOException
import java.net.Inet4Address
import java.net.InetAddress
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import javax.jmdns.JmDNS
import javax.jmdns.ServiceInfo
import javax.jmdns.impl.ViewerMdns

/** Tracks Wi-Fi rather than the default network, which may be cellular or a VPN.
 * No process-wide network binding: Nest's outgoing WebRTC is left alone.
 */
class LocalDiscovery(context: Context) : Closeable {
    private val connectivity = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
    private val wifi = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
    private val prefs = context.getSharedPreferences("local-discovery", Context.MODE_PRIVATE)
    private val gate = Any()
    private val networks = linkedMapOf<Network, String>()
    private data class Endpoint(val network: Network, val ip: String)
    private var desired: Endpoint? = null
    private var closed = false
    private val worker = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "CamFreeNest-mDNS").apply { isDaemon = true } }
    // Accessed only on worker. The callback revokes HTTP immediately before a
    // potentially slow mDNS goodbye/close; it never waits for multicast work.
    private var current: Endpoint? = null
    private var dns: ViewerMdns? = null
    private var discoveryStarted = 0L
    private var lastFailure = "none"
    private val failedDNS = AtomicReference<JmDNS?>()
    private var multicast: WifiManager.MulticastLock? = null
    private var nextAttempt = 0L
    private var registered = false

    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) {
            val ip = properties.linkAddresses.map { it.address }
                .filterIsInstance<Inet4Address>().firstOrNull { it.isSiteLocalAddress }?.hostAddress
            synchronized(gate) {
                if (closed) return
                if (ip == null) networks.remove(network) else networks[network] = ip
                selectNetwork()
            }
        }
        override fun onLost(network: Network) {
            synchronized(gate) {
                if (closed) return
                networks.remove(network)
                selectNetwork()
            }
        }
    }

    // Called with gate held. A network change closes existing remote HTTP
    // connections immediately, even if multicast registration is still running.
    private fun selectNetwork() {
        val entry = networks.entries.lastOrNull()
        val target = entry?.let { Endpoint(it.key, it.value) }
        if (target == desired) return
        desired = target
        Engine.setViewerNetwork("", "", if (target == null) "Connect to Wi-Fi to share the viewer locally." else "Wi-Fi changed; refreshing the local viewer address…")
    }

    init {
        Engine.setViewerNetwork("", "", "Waiting for a Wi-Fi connection…")
        val request = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        try {
            connectivity.registerNetworkCallback(request, callback)
            registered = true
        } catch (e: Exception) {
            Engine.setViewerNetwork("", "", "Wi-Fi discovery could not start: ${e.message}")
        }
        worker.scheduleWithFixedDelay({
            try { reconcile() } catch (e: Exception) {
                synchronized(gate) {
                    lastFailure = "${e.javaClass.simpleName}: ${e.message}"
                    if (!closed) Engine.setViewerNetwork(desired?.ip ?: "", "", "Local name unavailable; use the numeric link. Retrying…\nError: $lastFailure")
                }
                disposeDNS()
                nextAttempt = System.currentTimeMillis() + 15000
            }
        }, 0, 3, TimeUnit.SECONDS)
    }

    private fun reconcile() {
        val failed = failedDNS.getAndSet(null)
        if (failed != null && failed === dns) throw IOException("mDNS socket recovery failed")
        val target = synchronized(gate) { if (closed) null else desired }
        if (target != current) {
            disposeDNS()
            current = target
            nextAttempt = 0
        }
        if (target == null) return
        if (dns == null && System.currentTimeMillis() >= nextAttempt) {
            synchronized(gate) {
                if (closed || desired != target) return
                Engine.setViewerNetwork(target.ip, "", "Starting local name discovery; numeric link is available.")
            }
            val held = wifi.createMulticastLock("camfreenest:mdns").apply { setReferenceCounted(false); acquire() }
            multicast = held
            val preferred = prefs.getString("hostname", "camfreenest") ?: "camfreenest"
            // Supplying a named InetAddress avoids an unnecessary reverse DNS lookup.
            val address = InetAddress.getByAddress(preferred, InetAddress.getByName(target.ip).address)
            val responder = ViewerMdns(address, preferred)
            discoveryStarted = SystemClock.elapsedRealtime()
            dns = responder
            responder.setDelegate { failed, _ -> failedDNS.set(failed) }
            // Public DNS-SD attributes deliberately contain no pairing key.
            responder.registerService(ServiceInfo.create("_http._tcp.local.", "CamSaver viewer", 8767, 0, 0, mapOf("path" to "/", "pairing" to "required")))
        }
        val responder = dns ?: return
        if (responder.isClosed || responder.isClosing || responder.isCanceled) throw IOException("mDNS responder stopped")
        if (!responder.isAnnounced) {
            if (SystemClock.elapsedRealtime() - discoveryStarted > 20000) throw IOException("mDNS name announcement did not complete; ${responder.lastSendError}")
            synchronized(gate) {
                if (!closed && desired == target) Engine.setViewerNetwork(target.ip, "", "Checking the local name; numeric link is available.\n${responder.diagnostics()}\nLast failure: $lastFailure")
            }
            return
        }
        val hostname = responder.hostName.trimEnd('.')
        synchronized(gate) {
            if (closed || desired != target) return
            val error = Engine.setViewerNetwork(target.ip, hostname,
                "Name advertised: http://$hostname:8767/ — open the private link on the other device to test it.\nWi-Fi IPv4: ${target.ip}\n${responder.diagnostics()}\nLast failure: $lastFailure\nAdvertisement alone does not confirm reachability from another device.")
            if (error.isNotEmpty()) return // Retry HTTP binding on the next pass.
            // Persist collision-renamed hosts, so reconnects keep the same name.
            val label = hostname.removeSuffix(".local")
            if (prefs.getString("hostname", "camfreenest") != label) prefs.edit().putString("hostname", label).apply()
        }
    }

    private fun disposeDNS() {
        try { dns?.setDelegate(null); dns?.close() } catch (_: Exception) {
            // A departed interface may already have invalidated its socket.
        } finally {
            dns = null
            multicast?.let { if (it.isHeld) it.release() }
            multicast = null
        }
    }

    override fun close() {
        synchronized(gate) {
            closed = true
            desired = null
            Engine.setViewerNetwork("", "", "Wi-Fi viewer stopped.")
        }
        if (registered) connectivity.unregisterNetworkCallback(callback)
        // Serialize cleanup after any outstanding registration, away from main.
        worker.execute { disposeDNS() }
        worker.shutdown()
    }
}

package org.camfreenest.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Intent
import android.os.IBinder
import android.os.PowerManager
import engine.Engine
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class RecorderService : Service() {
    private var lock: PowerManager.WakeLock? = null
    private var discovery: LocalDiscovery? = null
    private var previewWorker: PreviewWorker? = null
    private var motionWorker: MotionWorker? = null
    private val previews = Executors.newSingleThreadScheduledExecutor()
    override fun onCreate() {
        super.onCreate()
        val manager = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        manager.createNotificationChannel(
            NotificationChannel("camfreenest", "Cam Motion Saver recorder", NotificationManager.IMPORTANCE_LOW)
        )
        val notification = Notification.Builder(this, "camfreenest")
            .setContentTitle("Cam Motion Saver")
            .setContentText("Local recording service is active")
            .setSmallIcon(R.drawable.ic_notification)
            .setOngoing(true)
            .build()
        startForeground(124, notification)
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        lock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "camfreenest:receiver")
        lock?.setReferenceCounted(false)
        lock?.acquire()
        discovery = LocalDiscovery(this)
        Thread({ Engine.start(filesDir.absolutePath) }, "CamFreeNest-Go").start()
        motionWorker = MotionWorker().also { it.start() }
        val worker = PreviewWorker(File(filesDir, "clips"))
        previewWorker = worker
        previews.scheduleWithFixedDelay(worker, 5, 10, TimeUnit.SECONDS)
    }
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int) = START_STICKY
    override fun onBind(intent: Intent?): IBinder? = null
    override fun onDestroy() {
        discovery?.close()
        previews.shutdownNow()
        previewWorker?.close()
        motionWorker?.close()
        Engine.stop()
        lock?.let { if (it.isHeld) it.release() }
        super.onDestroy()
    }
}

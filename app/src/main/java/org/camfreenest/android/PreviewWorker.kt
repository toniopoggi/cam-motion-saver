package org.camfreenest.android

import android.graphics.Bitmap
import android.media.MediaMetadataRetriever
import android.util.Log
import engine.Engine
import org.json.JSONArray
import java.io.ByteArrayOutputStream
import java.io.DataOutputStream
import java.io.File
import java.io.Closeable
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.ThreadPoolExecutor
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException

/** Bounded background thumbnail work; never blocks the WebRTC ingest thread. */
class PreviewWorker(private val clips: File) : Runnable, Closeable {
    private val decoder = ThreadPoolExecutor(1, 1, 0L, TimeUnit.MILLISECONDS, LinkedBlockingQueue<Runnable>()) { task ->
        Thread(task, "CamFreeNest-preview-decode").apply { isDaemon = true }
    }
    override fun close() { decoder.shutdownNow() }
    override fun run() {
        try {
            val jobs = JSONArray(Engine.claimPreviewJobs())
            for (i in 0 until jobs.length()) {
                if (Thread.currentThread().isInterrupted) return
                val name = jobs.getString(i)
                val task = decoder.submit<ByteArray> { create(File(clips, name)) }
                try {
                    val payload = task.get(15, TimeUnit.SECONDS)
                    val error = Engine.savePreview(name, payload)
                    check(error.isEmpty()) { error }
                }
                catch (e: Exception) {
                    if (e is InterruptedException) { Thread.currentThread().interrupt(); return }
                    val message = if (e is TimeoutException) "Preview decoding timed out after 15 seconds" else
                        (e.cause?.message ?: e.message ?: "Android could not decode this recording")
                    Engine.previewFailed(name, message)
                    Log.w("CamFreeNest", "Preview failed for $name", e)
                } finally { task.cancel(true); decoder.purge() }
            }
        } catch (e: Exception) { Log.w("CamFreeNest", "Preview scan failed", e) }
    }
    private fun create(file: File): ByteArray {
        val reader = MediaMetadataRetriever()
        try {
            reader.setDataSource(file.absolutePath)
            val durationUs = ((reader.extractMetadata(MediaMetadataRetriever.METADATA_KEY_DURATION)
                ?.toLongOrNull() ?: 0L) * 1000L).coerceAtLeast(1L)
            val payload = ByteArrayOutputStream()
            val out = DataOutputStream(payload)
            var count = 0
            for (i in 0 until 6) {
                if (Thread.currentThread().isInterrupted) throw InterruptedException()
                val at = ((i * 2L + 1L) * durationUs / 12L).coerceAtMost(durationUs - 1L)
                val frame = decode(reader, at, MediaMetadataRetriever.OPTION_CLOSEST)
                    ?: decode(reader, at, MediaMetadataRetriever.OPTION_CLOSEST_SYNC)
                    ?: continue
                append(frame, out); count++
            }
            // Tiny clips sometimes have only a single usable keyframe.
            if (count == 0) {
                val frame = decode(reader, 0L, MediaMetadataRetriever.OPTION_CLOSEST_SYNC)
                    ?: throw IllegalStateException("No frame could be decoded; the MP4 may be incomplete")
                append(frame, out)
            }
            return payload.toByteArray()
        } finally { reader.release() }
    }
    private fun decode(reader: MediaMetadataRetriever, at: Long, option: Int): Bitmap? {
        return try { reader.getScaledFrameAtTime(at, option, 320, 180) } catch (_: RuntimeException) { null }
    }
    private fun append(frame: Bitmap, out: DataOutputStream) {
        try {
            val jpeg = ByteArrayOutputStream()
            check(frame.compress(Bitmap.CompressFormat.JPEG, 65, jpeg))
            val bytes = jpeg.toByteArray(); out.writeInt(bytes.size); out.write(bytes)
        } finally { frame.recycle() }
    }
}

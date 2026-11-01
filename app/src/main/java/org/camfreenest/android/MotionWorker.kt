package org.camfreenest.android

import android.graphics.ImageFormat
import android.graphics.Bitmap
import android.media.Image
import java.io.ByteArrayOutputStream
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaCodecList
import android.media.MediaFormat
import android.os.Build
import engine.Engine
import java.io.Closeable
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.atomic.AtomicBoolean

/** Decode the existing H264 stream on a separate worker; never block recording. */
class MotionWorker : Closeable {
    private val running = AtomicBoolean(true)
    private var thread: Thread? = null
    private var decoder: MediaCodec? = null
    private var generation = -1L
    private var lastSample = 0L
    private var lastPreview = 0L
    private val info = MediaCodec.BufferInfo()
    private var lastErrorAt = 0L
    fun start() { thread = Thread({ run() }, "CamMotionSaver-detection").apply { isDaemon = true; start() } }
    override fun close() { running.set(false); thread?.interrupt(); try { thread?.join(1500) } catch (_: InterruptedException) { Thread.currentThread().interrupt() } }
    private fun release() { try { decoder?.stop() } catch (_: Exception) {} ; try { decoder?.release() } catch (_: Exception) {}; decoder = null; generation = -1; lastSample = 0; lastPreview = 0 }
    private fun decoderName(): String? = MediaCodecList(MediaCodecList.ALL_CODECS).codecInfos.firstOrNull { c ->
        !c.isEncoder && c.supportedTypes.any { it.equals("video/avc", true) } &&
        if (Build.VERSION.SDK_INT >= 29) c.isHardwareAccelerated else !c.name.startsWith("OMX.google.",true) && !c.name.startsWith("c2.android.",true)
    }?.name
    private fun run() {
        try {
            while (running.get()) {
                if (!Engine.motionEnabled()) { release(); Thread.sleep(250); continue }
                val bytes = Engine.nextMotionPacket() ?: continue
                try {
                    if (bytes.size <= 20) continue
                    val packet=ByteBuffer.wrap(bytes).order(ByteOrder.BIG_ENDIAN)
                    val gen=packet.long; val pts=packet.long; val width=packet.short.toInt() and 65535; val height=packet.short.toInt() and 65535
                    if (gen != generation) {
                        release()
                        val name=decoderName() ?: throw IllegalStateException("No hardware H.264 decoder available")
                        decoder=MediaCodec.createByCodecName(name)
                        val format=MediaFormat.createVideoFormat("video/avc",width,height)
                        format.setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatYUV420Flexible)
                        decoder!!.configure(format,null,null,0);decoder!!.start();generation=gen
                        Engine.motionStatus("Phone decoder connected; waiting for frames")
                    }
                    val codec=decoder ?: continue
                    // Input can legitimately be busy for more than 2 ms. Keep the
                    // same access unit, drain output, and abort if the stream epoch
                    // was reset while we waited. Never silently skip dependent H264 frames.
                    val input=DecoderInputWaiter.waitForInput(
                        { codec.dequeueInputBuffer(10_000) }, { drain(codec) },
                        { running.get() && Engine.motionGenerationCurrent(gen) },
                        { System.nanoTime() }, 1_000_000_000L)
                    if (input == DecoderInputWaiter.STALE) { release(); continue }
                    if (input == DecoderInputWaiter.TIMED_OUT) {
                        Engine.motionStatus("Phone motion unavailable: decoder input stalled; retrying")
                        Engine.resetMotionDecoder(); release(); continue
                    }
                    val buffer=codec.getInputBuffer(input) ?: throw IllegalStateException("Decoder input unavailable")
                    buffer.clear(); if (buffer.remaining() < packet.remaining()) throw IllegalStateException("Video frame too large for decoder")
                    buffer.put(packet);codec.queueInputBuffer(input,0,buffer.position(),pts,0)
                    drain(codec)
                } catch (e: Exception) {
                    val now=System.currentTimeMillis()
                    if (now-lastErrorAt>10_000) { Engine.motionStatus("Phone motion unavailable: "+(e.message ?: "decoder error"));lastErrorAt=now }
                    Engine.resetMotionDecoder();release();Thread.sleep(5000)
                }
            }
        } catch (_: InterruptedException) { Thread.currentThread().interrupt() }
        finally { release() }
    }
    private fun drain(codec: MediaCodec) {
        while (true) {
            val index=codec.dequeueOutputBuffer(info,0)
            if (index < 0) return
            try {
                val now=System.nanoTime()
                if (now-lastSample >= 500_000_000L && info.size > 0) {
                    val image=codec.getOutputImage(index) ?: throw IllegalStateException("Hardware decoder cannot expose frames")
                    try {
                        if (image.format != ImageFormat.YUV_420_888) throw IllegalStateException("Unsupported decoder frame format")
                        val plane=image.planes[0];val data=plane.buffer;val rect=image.cropRect
                        val frame=ByteArray(96*54)
                        for(y in 0 until 54) for(x in 0 until 96) {
                            val sx=rect.left + (x*rect.width()/96);val sy=rect.top+(y*rect.height()/54)
                            val at=data.position()+sy*plane.rowStride+sx*plane.pixelStride
                            if(at<0||at>=data.limit()) throw IllegalStateException("Invalid decoder frame stride")
                            frame[y*96+x]=data.get(at)
                        }
                        Engine.submitMotionFrame(frame,generation);lastSample=now
                        if (Engine.liveFrameRequested() && now-lastPreview>=1_000_000_000L) {
                            try { Engine.submitLiveFrame(makePreview(image),generation);lastPreview=now }
                            catch (_: Exception) { Engine.livePreviewFailed() }
                        }
                    } finally { image.close() }
                }
            } finally { codec.releaseOutputBuffer(index,false) }
        }
    }
    private fun makePreview(image: Image): ByteArray {
        val rect=image.cropRect;val rw=rect.width();val rh=rect.height()
        require(rw>0 && rh>0 && image.planes.size>=3)
        val scale=minOf(320.0/rw,240.0/rh,1.0);val w=maxOf(1,(rw*scale).toInt());val h=maxOf(1,(rh*scale).toInt())
        val planes=image.planes;val buffers=planes.map {it.buffer};val starts=buffers.map {it.position()};val pixels=IntArray(w*h)
        fun read(plane:Int,x:Int,y:Int):Int {val p=planes[plane];val index=starts[plane]+y*p.rowStride+x*p.pixelStride;require(index>=0&&index<buffers[plane].limit());return buffers[plane].get(index).toInt() and 255}
        for(y in 0 until h) for(x in 0 until w){
            val sx=rect.left+x*rw/w;val sy=rect.top+y*rh/h
            val yy=(read(0,sx,sy)-16).coerceAtLeast(0);val u=read(1,sx/2,sy/2)-128;val v=read(2,sx/2,sy/2)-128
            val red=((298*yy+409*v+128) shr 8).coerceIn(0,255);val green=((298*yy-100*u-208*v+128) shr 8).coerceIn(0,255);val blue=((298*yy+516*u+128) shr 8).coerceIn(0,255)
            pixels[y*w+x]=(0xff shl 24) or (red shl 16) or (green shl 8) or blue
        }
        val bitmap=Bitmap.createBitmap(pixels,w,h,Bitmap.Config.ARGB_8888)
        try { val out=ByteArrayOutputStream();check(bitmap.compress(Bitmap.CompressFormat.JPEG,65,out));return out.toByteArray() } finally { bitmap.recycle() }
    }

}

package org.camfreenest.android

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient

class MainActivity : Activity() {
    private lateinit var web: WebView
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        startForegroundService(Intent(this, RecorderService::class.java))
        web = WebView(this)
        web.setBackgroundColor(android.graphics.Color.rgb(250, 248, 242))
        web.settings.javaScriptEnabled = true
        web.settings.domStorageEnabled = false
        web.settings.allowFileAccess = false
        web.settings.allowContentAccess = false
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val u = request.url
                if (u.host == "127.0.0.1" && u.port == 8767 && u.scheme == "http") return false
                if (u.scheme == "https" && u.host == "nestservices.google.com") {
                    startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(u.toString())))
                }
                return true
            }
        }
        setContentView(web)
        Handler(Looper.getMainLooper()).postDelayed({
            if (!isFinishing) web.loadUrl("http://127.0.0.1:8767/")
        }, 1600)
    }
    override fun onDestroy() {
        web.destroy()
        super.onDestroy()
    }
}

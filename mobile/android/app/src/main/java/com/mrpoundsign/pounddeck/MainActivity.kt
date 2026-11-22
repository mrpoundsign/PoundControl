package com.mrpoundsign.pounddeck

import android.annotation.SuppressLint
import android.content.Intent
import android.content.pm.ActivityInfo
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat

class MainActivity : AppCompatActivity() {

    companion object {
        private const val PREFS_NAME = "pounddeck_prefs"
        private const val AUTH_PREFS_NAME = "pounddeck_auth"
        private const val KEY_BASE_URL = "last_base_url"
        private const val KEY_ORIENTATION = "orientation_mode"
        private const val USER_AGENT_SUFFIX = "PoundDeckAndroid/1.0"
    }

    private lateinit var webView: WebView
    private lateinit var setupScroll: ScrollView
    private lateinit var errorOverlay: LinearLayout
    private lateinit var hostInput: EditText
    private lateinit var setupErrorText: TextView
    private lateinit var offlineDetailText: TextView

    private lateinit var orientAutoBtn: Button
    private lateinit var orientLandBtn: Button
    private lateinit var orientPortBtn: Button

    private var allowedBaseUrl: String? = null
    private var lastRequestedUrl: String? = null
    private var mainFrameFailed = false

    private val scannerLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode == RESULT_OK) {
            val scanned = result.data?.getStringExtra(QrScannerActivity.EXTRA_SCANNED_URL)
            if (!scanned.isNullOrBlank()) {
                connectToUrl(scanned)
            }
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // Keep Android display active whenever PoundDeck Companion is open (works over HTTP LAN).
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            window.attributes = window.attributes.apply {
                layoutInDisplayCutoutMode = WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES
            }
        }
        applyImmersiveMode()
        applyOrientation(getOrientationMode())

        val root = FrameLayout(this).apply {
            setBackgroundColor(Color.parseColor("#0A0A0C"))
        }

        webView = buildWebView()
        root.addView(
            webView,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )

        setupScroll = buildSetupView()
        root.addView(
            setupScroll,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )

        errorOverlay = buildOfflineView()
        root.addView(
            errorOverlay,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )

        ViewCompat.setOnApplyWindowInsetsListener(setupScroll) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            val pad = dp(20)
            v.setPadding(bars.left + pad, bars.top + pad, bars.right + pad, bars.bottom + pad)
            insets
        }
        ViewCompat.setOnApplyWindowInsetsListener(errorOverlay) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            val pad = dp(24)
            v.setPadding(bars.left + pad, bars.top + pad, bars.right + pad, bars.bottom + pad)
            insets
        }

        setContentView(root)

        onBackPressedDispatcher.addCallback(
            this,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    if (webView.visibility == View.VISIBLE && webView.canGoBack()) {
                        webView.goBack()
                    } else {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                    }
                }
            },
        )

        val savedBase = prefs().getString(KEY_BASE_URL, null)
        if (!savedBase.isNullOrBlank()) {
            hostInput.setText(savedBase.removeSuffix("/"))
            connectToUrl(savedBase)
        } else {
            showSetupScreen()
        }
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) {
            applyImmersiveMode()
        }
    }

    override fun onPause() {
        CookieManager.getInstance().flush()
        super.onPause()
    }

    override fun onDestroy() {
        CookieManager.getInstance().flush()
        webView.removeJavascriptInterface("PoundDeckNative")
        webView.destroy()
        super.onDestroy()
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun buildWebView(): WebView {
        val wv = WebView(this)
        wv.setBackgroundColor(Color.parseColor("#0A0A0C"))
        wv.isVerticalScrollBarEnabled = false
        wv.isHorizontalScrollBarEnabled = false
        wv.overScrollMode = View.OVER_SCROLL_NEVER

        val cm = CookieManager.getInstance()
        cm.setAcceptCookie(true)
        cm.setAcceptThirdPartyCookies(wv, true)

        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            allowContentAccess = false
            setSupportZoom(false)
            builtInZoomControls = false
            displayZoomControls = false
            mediaPlaybackRequiresUserGesture = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            userAgentString = "$userAgentString $USER_AGENT_SUFFIX"
        }

        wv.webChromeClient = WebChromeClient()
        wv.addJavascriptInterface(NativeBridge(), "PoundDeckNative")

        wv.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val target = request.url?.toString() ?: return true
                val base = allowedBaseUrl ?: return true
                return !DeckUrlValidator.isSameHost(target, base)
            }

            override fun onPageStarted(view: WebView, url: String?, favicon: android.graphics.Bitmap?) {
                if (url != null && url != "about:blank") {
                    mainFrameFailed = false
                }
            }

            override fun onPageFinished(view: WebView, url: String?) {
                if (url == null || url == "about:blank") {
                    return
                }
                CookieManager.getInstance().flush()
                if (!mainFrameFailed) {
                    showWebDeck()
                }
            }

            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: WebResourceError,
            ) {
                if (request.isForMainFrame) {
                    mainFrameFailed = true
                    showOfflineScreen("Could not reach ${lastRequestedUrl ?: "PoundDeck"} (${error.description})")
                }
            }
        }
        return wv
    }

    private fun buildSetupView(): ScrollView {
        val scroll = ScrollView(this).apply {
            isFillViewport = true
            setBackgroundColor(Color.parseColor("#0A0A0C"))
        }

        val centerWrap = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
        }

        val card = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(22), dp(22), dp(22), dp(22))
            background = GradientDrawable().apply {
                shape = GradientDrawable.RECTANGLE
                cornerRadius = dp(18).toFloat()
                setColor(Color.parseColor("#141416"))
                setStroke(dp(1), Color.parseColor("#475569"))
            }
        }

        val logo = ImageView(this).apply {
            setImageResource(R.drawable.pounddeck_logo)
            contentDescription = "PoundDeck"
        }
        card.addView(logo, LinearLayout.LayoutParams(dp(64), dp(64)))

        val title = TextView(this).apply {
            text = "PoundDeck Companion"
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 22f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setPadding(0, dp(12), 0, dp(6))
        }
        card.addView(title)

        val subtitle = TextView(this).apply {
            text = "Scan the QR code in PoundDeck Settings -> Web Deck, or enter your PC LAN address."
            setTextColor(Color.parseColor("#94A3B8"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, dp(18))
        }
        card.addView(subtitle)

        val scanBtn = makeActionButton("Scan QR Code", "#38BDF8", Color.parseColor("#020617")) {
            launchScanner()
        }
        card.addView(scanBtn, fullWidthParams(bottomMargin = dp(14)))

        val orLbl = TextView(this).apply {
            text = "OR CONNECT MANUALLY"
            setTextColor(Color.parseColor("#64748B"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 11f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, dp(8))
        }
        card.addView(orLbl)

        hostInput = EditText(this).apply {
            hint = "http://192.168.1.50:9120"
            setHintTextColor(Color.parseColor("#64748B"))
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            isSingleLine = true
            setPadding(dp(14), dp(12), dp(14), dp(12))
            background = GradientDrawable().apply {
                shape = GradientDrawable.RECTANGLE
                cornerRadius = dp(12).toFloat()
                setColor(Color.parseColor("#020203"))
                setStroke(dp(1), Color.parseColor("#475569"))
            }
        }
        card.addView(hostInput, fullWidthParams(bottomMargin = dp(10)))

        setupErrorText = TextView(this).apply {
            setTextColor(Color.parseColor("#FCA5A5"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 13f)
            visibility = View.GONE
            setPadding(0, 0, 0, dp(10))
        }
        card.addView(setupErrorText, fullWidthParams())

        val connectBtn = makeActionButton("Connect", "#1E293B", Color.WHITE) {
            val raw = hostInput.text?.toString().orEmpty()
            val normalized = DeckUrlValidator.normalizeUrl(raw)
            if (normalized == null) {
                setupErrorText.text = "Enter a valid HTTP/HTTPS LAN URL or IP address (e.g. 192.168.1.50:9120)."
                setupErrorText.visibility = View.VISIBLE
            } else {
                setupErrorText.visibility = View.GONE
                connectToUrl(normalized)
            }
        }
        card.addView(connectBtn, fullWidthParams(bottomMargin = dp(18)))

        val orientLbl = TextView(this).apply {
            text = "Screen Orientation Lock"
            setTextColor(Color.parseColor("#94A3B8"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, dp(8))
        }
        card.addView(orientLbl)

        val orientRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
        }
        orientAutoBtn = makeSegmentButton("Auto") { updateOrientation("auto") }
        orientLandBtn = makeSegmentButton("Landscape") { updateOrientation("landscape") }
        orientPortBtn = makeSegmentButton("Portrait") { updateOrientation("portrait") }

        val segLp = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f).apply {
            setMargins(dp(3), 0, dp(3), 0)
        }
        orientRow.addView(orientAutoBtn, LinearLayout.LayoutParams(segLp))
        orientRow.addView(orientLandBtn, LinearLayout.LayoutParams(segLp))
        orientRow.addView(orientPortBtn, LinearLayout.LayoutParams(segLp))
        card.addView(orientRow, fullWidthParams())

        refreshOrientationButtons(getOrientationMode())

        val cardLp = LinearLayout.LayoutParams(dp(360), LinearLayout.LayoutParams.WRAP_CONTENT)
        centerWrap.addView(card, cardLp)
        scroll.addView(centerWrap)
        return scroll
    }

    private fun buildOfflineView(): LinearLayout {
        val overlay = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setBackgroundColor(Color.parseColor("#0A0A0C"))
            visibility = View.GONE
        }

        val title = TextView(this).apply {
            text = "Deck Unreachable"
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 20f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, dp(8))
        }
        overlay.addView(title)

        offlineDetailText = TextView(this).apply {
            text = "Make sure PoundDeck is running on your PC with Web Deck enabled on the same Wi-Fi network."
            setTextColor(Color.parseColor("#94A3B8"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, dp(20))
        }
        overlay.addView(offlineDetailText)

        val retryBtn = makeActionButton("Retry Connection", "#38BDF8", Color.parseColor("#020617")) {
            val target = lastRequestedUrl ?: allowedBaseUrl
            if (target != null) {
                connectToUrl(target)
            } else {
                showSetupScreen()
            }
        }
        overlay.addView(retryBtn, LinearLayout.LayoutParams(dp(260), LinearLayout.LayoutParams.WRAP_CONTENT).apply {
            bottomMargin = dp(10)
        })

        val rescanBtn = makeActionButton("Scan New QR", "#1E293B", Color.WHITE) {
            launchScanner()
        }
        overlay.addView(rescanBtn, LinearLayout.LayoutParams(dp(260), LinearLayout.LayoutParams.WRAP_CONTENT).apply {
            bottomMargin = dp(10)
        })

        val changeBtn = makeActionButton("Change Host", "#141416", Color.WHITE) {
            showSetupScreen()
        }
        overlay.addView(changeBtn, LinearLayout.LayoutParams(dp(260), LinearLayout.LayoutParams.WRAP_CONTENT))

        return overlay
    }

    private fun connectToUrl(rawUrl: String) {
        val normalized = DeckUrlValidator.normalizeUrl(rawUrl)
        val baseOrigin = normalized?.let { DeckUrlValidator.baseOriginUrl(it) }
        if (normalized == null || baseOrigin == null) {
            showSetupScreen()
            setupErrorText.text = "Invalid server URL."
            setupErrorText.visibility = View.VISIBLE
            return
        }

        // Persist only the stripped origin so one-time ?pair= tokens are never replayed later.
        allowedBaseUrl = baseOrigin
        lastRequestedUrl = normalized
        prefs().edit().putString(KEY_BASE_URL, baseOrigin).apply()
        hostInput.setText(baseOrigin.removeSuffix("/"))

        mainFrameFailed = false
        setupScroll.visibility = View.GONE
        errorOverlay.visibility = View.GONE
        webView.visibility = View.VISIBLE
        webView.loadUrl(normalized)
    }

    private fun showSetupScreen() {
        setupScroll.visibility = View.VISIBLE
        errorOverlay.visibility = View.GONE
        webView.visibility = View.GONE
    }

    private fun showOfflineScreen(detail: String) {
        offlineDetailText.text = detail
        errorOverlay.visibility = View.VISIBLE
        setupScroll.visibility = View.GONE
        webView.visibility = View.GONE
    }

    private fun showWebDeck() {
        webView.visibility = View.VISIBLE
        setupScroll.visibility = View.GONE
        errorOverlay.visibility = View.GONE
    }

    private fun launchScanner() {
        scannerLauncher.launch(Intent(this, QrScannerActivity::class.java))
    }

    private fun getOrientationMode(): String {
        return prefs().getString(KEY_ORIENTATION, "auto") ?: "auto"
    }

    private fun updateOrientation(mode: String) {
        val clean = when (mode) {
            "landscape", "portrait" -> mode
            else -> "auto"
        }
        prefs().edit().putString(KEY_ORIENTATION, clean).apply()
        applyOrientation(clean)
        refreshOrientationButtons(clean)
    }

    private fun applyOrientation(mode: String) {
        requestedOrientation = when (mode) {
            "landscape" -> ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE
            "portrait" -> ActivityInfo.SCREEN_ORIENTATION_SENSOR_PORTRAIT
            else -> ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
        }
    }

    private fun refreshOrientationButtons(active: String) {
        styleSegment(orientAutoBtn, active == "auto")
        styleSegment(orientLandBtn, active == "landscape")
        styleSegment(orientPortBtn, active == "portrait")
    }

    private fun styleSegment(btn: Button, selected: Boolean) {
        btn.setTextColor(if (selected) Color.parseColor("#020617") else Color.WHITE)
        btn.background = GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            cornerRadius = dp(10).toFloat()
            setColor(Color.parseColor(if (selected) "#38BDF8" else "#1E293B"))
            setStroke(dp(1), Color.parseColor(if (selected) "#38BDF8" else "#475569"))
        }
    }

    private fun applyImmersiveMode() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        val controller = WindowInsetsControllerCompat(window, window.decorView)
        controller.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        controller.hide(WindowInsetsCompat.Type.systemBars())
    }

    private fun prefs() = getSharedPreferences(PREFS_NAME, MODE_PRIVATE)

    private fun authPrefs() = getSharedPreferences(AUTH_PREFS_NAME, MODE_PRIVATE)

    private fun makeActionButton(label: String, bgHex: String, fgColor: Int, onClick: () -> Unit): Button {
        return Button(this).apply {
            text = label
            isAllCaps = false
            setTextColor(fgColor)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(dp(16), dp(12), dp(16), dp(12))
            background = GradientDrawable().apply {
                shape = GradientDrawable.RECTANGLE
                cornerRadius = dp(12).toFloat()
                setColor(Color.parseColor(bgHex))
                setStroke(dp(1), Color.parseColor("#475569"))
            }
            setOnClickListener { onClick() }
        }
    }

    private fun makeSegmentButton(label: String, onClick: () -> Unit): Button {
        return Button(this).apply {
            text = label
            isAllCaps = false
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(dp(8), dp(10), dp(8), dp(10))
            setOnClickListener { onClick() }
        }
    }

    private fun fullWidthParams(bottomMargin: Int = 0): LinearLayout.LayoutParams {
        return LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT,
            LinearLayout.LayoutParams.WRAP_CONTENT,
        ).apply {
            this.bottomMargin = bottomMargin
        }
    }

    private fun dp(value: Int): Int {
        return TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_DIP,
            value.toFloat(),
            resources.displayMetrics,
        ).toInt()
    }

    inner class NativeBridge {
        @JavascriptInterface
        fun isNativeApp(): Boolean = true

        @JavascriptInterface
        fun openScanner() {
            runOnUiThread { launchScanner() }
        }

        @JavascriptInterface
        fun openSetup() {
            runOnUiThread { showSetupScreen() }
        }

        @JavascriptInterface
        fun getOrientation(): String = getOrientationMode()

        @JavascriptInterface
        fun setOrientation(mode: String) {
            runOnUiThread { updateOrientation(mode) }
        }

        @JavascriptInterface
        fun getAuthRecord(origin: String): String {
            val clean = DeckUrlValidator.baseOriginUrl(origin) ?: return ""
            return authPrefs().getString(clean, "") ?: ""
        }

        @JavascriptInterface
        fun saveAuthRecord(origin: String, json: String) {
            val clean = DeckUrlValidator.baseOriginUrl(origin) ?: return
            authPrefs().edit().putString(clean, json).apply()
        }

        @JavascriptInterface
        fun clearAuthRecord(origin: String) {
            val clean = DeckUrlValidator.baseOriginUrl(origin) ?: return
            authPrefs().edit().remove(clean).apply()
        }

        @JavascriptInterface
        fun getDeviceName(): String {
            val man = Build.MANUFACTURER.orEmpty().trim()
            val mod = Build.MODEL.orEmpty().trim()
            val raw = when {
                mod.isEmpty() -> "Android Companion"
                man.isNotEmpty() && !mod.startsWith(man, ignoreCase = true) -> "$man $mod"
                else -> mod
            }
            return raw.take(48)
        }
    }
}

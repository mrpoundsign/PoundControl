package com.mrpoundsign.poundcontrol.wear

import android.animation.ValueAnimator
import android.content.res.ColorStateList
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.TextUtils
import android.view.Gravity
import android.view.HapticFeedbackConstants
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.view.animation.AccelerateDecelerateInterpolator
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.core.view.InputDeviceCompat
import androidx.core.view.MotionEventCompat
import androidx.lifecycle.lifecycleScope
import com.google.android.gms.wearable.MessageClient
import com.google.android.gms.wearable.MessageEvent
import com.google.android.gms.wearable.Wearable
import com.mrpoundsign.poundcontrol.protocol.DeckCommand
import com.mrpoundsign.poundcontrol.protocol.DeckDial
import com.mrpoundsign.poundcontrol.protocol.DeckSlot
import com.mrpoundsign.poundcontrol.protocol.DeckSnapshot
import com.mrpoundsign.poundcontrol.protocol.WatchStatusMessage
import com.mrpoundsign.poundcontrol.protocol.WearProtocol
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.tasks.await
import kotlinx.coroutines.withContext
import kotlin.math.abs

/**
 * Wear OS deck controller activity communicating over Bluetooth Wear Data Layer with the
 * paired phone's [com.mrpoundsign.poundcontrol.WatchRelayService].
 */
class WearMainActivity : ComponentActivity(), MessageClient.OnMessageReceivedListener {

    private enum class Tab {
        KEYS,
        DIALS,
        MENU,
    }

    private companion object {
        private const val PREFS_NAME = "poundcontrol_wear_ui"
        private const val KEY_LAYOUT_MODE = "layout_mode"
        private const val KEY_KEEP_SCREEN_ON = "keep_screen_on"
        private const val PING_INTERVAL_MS = 20_000L
        private const val CROWN_BATCH_DELAY_MS = 55L
        private const val KEY_RELEASE_DELAY_MS = 65L
    }

    private val mainHandler = Handler(Looper.getMainLooper())
    private val prefs by lazy { getSharedPreferences(PREFS_NAME, MODE_PRIVATE) }

    private var currentTab: Tab = Tab.KEYS
    private var layoutMode: LayoutMode = LayoutMode.AUTO
    private var keepScreenOn: Boolean = false

    private var latestSnapshot: DeckSnapshot? = null
    private var latestStatus: WatchStatusMessage = WatchStatusMessage(
        state = WatchStatusMessage.STATE_CONNECTING,
        detail = "Connecting to phone relay...",
    )
    private var connectedPhoneNodeId: String? = null
    private var selectedDialIndex: Int = 0
    private var dialPressed: Boolean = false

    private var crownAccumulator: Float = 0f
    private var pendingCrownTicks: Int = 0
    private val flushCrownRunnable = Runnable { flushPendingCrownTicks() }

    private val activeGlowTiles = ArrayList<GlowTileView>()
    private var shimmerAnimator: ValueAnimator? = null
    private var isWindowActive: Boolean = false

    private var keepaliveJob: Job? = null

    private lateinit var tabKeysBtn: TextView
    private lateinit var tabDialsBtn: TextView
    private lateinit var tabMenuBtn: TextView
    private lateinit var headerStripView: TextView

    private lateinit var keysScrollView: ScrollView
    private lateinit var keysContainer: LinearLayout

    private lateinit var dialsScrollView: ScrollView
    private lateinit var dialsContainer: LinearLayout
    private lateinit var dialArcView: DialArcView
    private lateinit var dialSwitcherRow: LinearLayout
    private lateinit var dialMinusBtn: TextView
    private lateinit var dialCounterView: TextView
    private lateinit var dialPlusBtn: TextView
    private lateinit var dialsEmptyView: TextView
    private lateinit var dialsActiveGroup: LinearLayout

    private lateinit var menuScrollView: ScrollView
    private lateinit var menuContainer: LinearLayout

    private lateinit var statusOverlay: LinearLayout
    private lateinit var statusTitleView: TextView
    private lateinit var statusDetailView: TextView
    private lateinit var statusRetryBtn: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        layoutMode = LayoutMode.fromStorageKey(prefs.getString(KEY_LAYOUT_MODE, null))
        keepScreenOn = prefs.getBoolean(KEY_KEEP_SCREEN_ON, false)
        applyKeepScreenOnFlag()

        setContentView(buildContentView())
        renderAll()
    }

    override fun onResume() {
        super.onResume()
        isWindowActive = true
        applyKeepScreenOnFlag()
        Wearable.getMessageClient(this).addListener(this)
        startOpenAndKeepalive()
        syncShimmerAnimator()
    }

    override fun onPause() {
        isWindowActive = false
        syncShimmerAnimator()
        mainHandler.removeCallbacks(flushCrownRunnable)
        flushPendingCrownTicks()
        keepaliveJob?.cancel()
        keepaliveJob = null
        Wearable.getMessageClient(this).removeListener(this)
        sendCloseBestEffort()
        super.onPause()
    }

    override fun onMessageReceived(messageEvent: MessageEvent) {
        if (!messageEvent.path.startsWith(WearProtocol.PATH_PREFIX)) return
        connectedPhoneNodeId = messageEvent.sourceNodeId
        val payload = messageEvent.data.toString(Charsets.UTF_8)

        when (messageEvent.path) {
            WearProtocol.PATH_SNAPSHOT -> {
                val snap = DeckSnapshot.fromJsonString(payload) ?: return
                latestSnapshot = snap
                if (latestStatus.state == WatchStatusMessage.STATE_CONNECTING) {
                    latestStatus = WatchStatusMessage(WatchStatusMessage.STATE_LIVE, "Phone Relay")
                }
                ensureValidSelectedDial(snap)
                renderAll()
            }

            WearProtocol.PATH_STATUS -> {
                val msg = WatchStatusMessage.fromJson(payload) ?: return
                latestStatus = msg
                renderAll()
            }
        }
    }

    override fun dispatchGenericMotionEvent(ev: MotionEvent): Boolean {
        if (currentTab == Tab.DIALS &&
            ev.action == MotionEvent.ACTION_SCROLL &&
            ev.isFromSource(InputDeviceCompat.SOURCE_ROTARY_ENCODER) &&
            isLiveOrInteractive()
        ) {
            val delta = -ev.getAxisValue(MotionEventCompat.AXIS_SCROLL)
            val result = WearDeckHelper.accumulateCrownTicks(
                accum = crownAccumulator,
                delta = delta,
                threshold = 0.75f,
                maxStep = 20,
            )
            crownAccumulator = result.remainingAccum
            if (result.ticks != 0) {
                queueCrownTicks(result.ticks)
                dialArcView.performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
            }
            return true
        }
        return super.dispatchGenericMotionEvent(ev)
    }

    private fun isLiveOrInteractive(): Boolean {
        return currentTab == Tab.DIALS &&
            latestSnapshot?.dialSlots?.any { it.assigned } == true &&
            latestStatus.state == WatchStatusMessage.STATE_LIVE
    }

    private fun startOpenAndKeepalive() {
        keepaliveJob?.cancel()
        keepaliveJob = lifecycleScope.launch {
            val nodeId = resolvePhoneNodeId()
            if (nodeId == null) {
                latestStatus = WatchStatusMessage(
                    state = WatchStatusMessage.STATE_OFFLINE,
                    detail = "No paired phone found over Bluetooth",
                )
                renderAll()
                return@launch
            }
            connectedPhoneNodeId = nodeId
            if (latestSnapshot == null || latestStatus.state != WatchStatusMessage.STATE_LIVE) {
                latestStatus = WatchStatusMessage(
                    state = WatchStatusMessage.STATE_CONNECTING,
                    detail = "Connecting via phone relay...",
                )
                renderAll()
            }

            val opened = sendWearMessage(nodeId, WearProtocol.PATH_OPEN, ByteArray(0))
            if (!opened) {
                latestStatus = WatchStatusMessage(
                    state = WatchStatusMessage.STATE_OFFLINE,
                    detail = "Unable to reach phone over Bluetooth",
                )
                renderAll()
                return@launch
            }

            while (isActive) {
                delay(PING_INTERVAL_MS)
                val activeNode = connectedPhoneNodeId ?: resolvePhoneNodeId() ?: continue
                connectedPhoneNodeId = activeNode
                sendWearMessage(activeNode, WearProtocol.PATH_PING, ByteArray(0))
            }
        }
    }

    private fun reconnect() {
        latestStatus = WatchStatusMessage(
            state = WatchStatusMessage.STATE_CONNECTING,
            detail = "Reconnecting to phone relay...",
        )
        renderAll()
        startOpenAndKeepalive()
    }

    private fun sendCloseBestEffort() {
        val nodeId = connectedPhoneNodeId ?: return
        Wearable.getMessageClient(applicationContext)
            .sendMessage(nodeId, WearProtocol.PATH_CLOSE, ByteArray(0))
    }

    private fun sendDeckCommand(cmdJson: String) {
        lifecycleScope.launch {
            val nodeId = connectedPhoneNodeId ?: resolvePhoneNodeId()
            if (nodeId == null) {
                latestStatus = WatchStatusMessage(
                    state = WatchStatusMessage.STATE_OFFLINE,
                    detail = "Phone disconnected over Bluetooth",
                )
                renderAll()
                return@launch
            }
            connectedPhoneNodeId = nodeId
            val sent = sendWearMessage(nodeId, WearProtocol.PATH_CMD, cmdJson.toByteArray(Charsets.UTF_8))
            if (!sent) {
                latestStatus = WatchStatusMessage(
                    state = WatchStatusMessage.STATE_OFFLINE,
                    detail = "Bluetooth command delivery failed",
                )
                renderAll()
            }
        }
    }

    private suspend fun resolvePhoneNodeId(): String? = withContext(Dispatchers.IO) {
        try {
            val nodes = Wearable.getNodeClient(this@WearMainActivity).connectedNodes.await()
            nodes.firstOrNull { it.isNearby }?.id ?: nodes.firstOrNull()?.id
        } catch (_: Exception) {
            null
        }
    }

    private suspend fun sendWearMessage(nodeId: String, path: String, payload: ByteArray): Boolean {
        return withContext(Dispatchers.IO) {
            try {
                Wearable.getMessageClient(this@WearMainActivity)
                    .sendMessage(nodeId, path, payload)
                    .await()
                true
            } catch (_: Exception) {
                false
            }
        }
    }

    private fun queueCrownTicks(ticks: Int) {
        pendingCrownTicks = (pendingCrownTicks + ticks).coerceIn(-20, 20)
        if (abs(pendingCrownTicks) >= 10) {
            mainHandler.removeCallbacks(flushCrownRunnable)
            flushPendingCrownTicks()
        } else {
            mainHandler.removeCallbacks(flushCrownRunnable)
            mainHandler.postDelayed(flushCrownRunnable, CROWN_BATCH_DELAY_MS)
        }
    }

    private fun flushPendingCrownTicks() {
        val ticks = pendingCrownTicks
        if (ticks == 0) return
        pendingCrownTicks = 0
        val dial = currentSelectedDial() ?: return
        sendDeckCommand(DeckCommand.dialRotate(dial.index, ticks))
    }

    private fun ensureValidSelectedDial(snap: DeckSnapshot) {
        if (snap.dialSlots.isEmpty()) {
            selectedDialIndex = 0
            return
        }
        val existing = snap.dialSlots.firstOrNull { it.index == selectedDialIndex }
        if (existing != null) return
        selectedDialIndex = snap.dialSlots.firstOrNull { it.assigned }?.index ?: snap.dialSlots.first().index
    }

    private fun currentSelectedDial(): DeckDial? {
        val dials = latestSnapshot?.dialSlots ?: return null
        if (dials.isEmpty()) return null
        return dials.firstOrNull { it.index == selectedDialIndex } ?: dials.first()
    }

    private fun applyKeepScreenOnFlag() {
        if (keepScreenOn) {
            window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        } else {
            window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        }
    }

    private fun buildContentView(): View {
        val isRound = resources.configuration.isScreenRound
        val topPad = dp(if (isRound) 10 else 6)
        val sidePad = dp(if (isRound) 16 else 8)
        val bottomScrollPad = dp(if (isRound) 28 else 12)

        val root = FrameLayout(this).apply {
            setBackgroundColor(WearDeckHelper.OBSIDIAN_SLATE_ARGB)
            clipChildren = false
        }

        val mainColumn = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            clipChildren = false
            clipToPadding = false
            setPadding(sidePad, topPad, sidePad, 0)
        }

        val tabBar = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            )
        }
        tabKeysBtn = createTabPill("Keys") { switchTab(Tab.KEYS) }
        tabDialsBtn = createTabPill("Dials") { switchTab(Tab.DIALS) }
        tabMenuBtn = createTabPill("Menu") { switchTab(Tab.MENU) }
        tabBar.addView(tabKeysBtn)
        tabBar.addView(tabDialsBtn)
        tabBar.addView(tabMenuBtn)
        mainColumn.addView(tabBar)

        headerStripView = TextView(this).apply {
            textSize = 10f
            setTextColor(0xFF94A3B8.toInt())
            gravity = Gravity.CENTER
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.END
            setPadding(dp(4), dp(4), dp(4), dp(4))
        }
        mainColumn.addView(
            headerStripView,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ),
        )

        val contentStack = FrameLayout(this).apply {
            clipChildren = false
            clipToPadding = false
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                0,
                1f,
            )
        }

        keysContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            clipChildren = false
            clipToPadding = false
            setPadding(dp(4), dp(4), dp(4), bottomScrollPad)
        }
        keysScrollView = ScrollView(this).apply {
            isVerticalScrollBarEnabled = false
            clipChildren = false
            clipToPadding = false
            addView(
                keysContainer,
                ViewGroup.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ),
            )
        }
        contentStack.addView(keysScrollView)

        dialsContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            clipChildren = false
            clipToPadding = false
            setPadding(0, dp(2), 0, bottomScrollPad)
        }
        dialsEmptyView = TextView(this).apply {
            text = "No dials configured"
            textSize = 12f
            setTextColor(0xFF94A3B8.toInt())
            gravity = Gravity.CENTER
            setPadding(dp(8), dp(28), dp(8), dp(8))
        }
        dialsActiveGroup = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            clipChildren = false
            clipToPadding = false
        }
        dialSwitcherRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
            setPadding(0, 0, 0, dp(4))
        }
        dialArcView = DialArcView(this).apply {
            val size = dp(124)
            layoutParams = LinearLayout.LayoutParams(size, size)
            setOnClickListener {
                val dial = currentSelectedDial() ?: return@setOnClickListener
                if (!dial.assigned) return@setOnClickListener
                performHapticFeedback(HapticFeedbackConstants.CONFIRM)
                dialPressed = true
                bind(dial, isPressed = true)
                mainHandler.postDelayed(
                    {
                        dialPressed = false
                        bind(currentSelectedDial(), isPressed = false)
                    },
                    140L,
                )
                sendDeckCommand(DeckCommand.dialPress(dial.index))
            }
        }
        val dialStepRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
            setPadding(0, dp(4), 0, 0)
        }
        dialMinusBtn = createActionPill("−", bgArgb = 0xFF161922.toInt(), fgArgb = WearDeckHelper.CHROME_WHITE_ARGB) {
            val dial = currentSelectedDial() ?: return@createActionPill
            if (!dial.assigned) return@createActionPill
            it.performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
            sendDeckCommand(DeckCommand.dialRotate(dial.index, -1))
        }
        dialCounterView = TextView(this).apply {
            textSize = 10.5f
            setTextColor(0xFF94A3B8.toInt())
            gravity = Gravity.CENTER
            setPadding(dp(10), 0, dp(10), 0)
        }
        dialPlusBtn = createActionPill("+", bgArgb = 0xFF161922.toInt(), fgArgb = WearDeckHelper.CHROME_WHITE_ARGB) {
            val dial = currentSelectedDial() ?: return@createActionPill
            if (!dial.assigned) return@createActionPill
            it.performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
            sendDeckCommand(DeckCommand.dialRotate(dial.index, +1))
        }
        dialStepRow.addView(dialMinusBtn)
        dialStepRow.addView(dialCounterView)
        dialStepRow.addView(dialPlusBtn)

        dialsActiveGroup.addView(dialSwitcherRow)
        dialsActiveGroup.addView(dialArcView)
        dialsActiveGroup.addView(dialStepRow)

        dialsContainer.addView(dialsEmptyView)
        dialsContainer.addView(dialsActiveGroup)
        dialsScrollView = ScrollView(this).apply {
            isVerticalScrollBarEnabled = false
            addView(
                dialsContainer,
                ViewGroup.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ),
            )
        }
        contentStack.addView(dialsScrollView)

        menuContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, dp(2), 0, bottomScrollPad)
        }
        menuScrollView = ScrollView(this).apply {
            isVerticalScrollBarEnabled = false
            addView(
                menuContainer,
                ViewGroup.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ),
            )
        }
        contentStack.addView(menuScrollView)

        statusOverlay = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setBackgroundColor(0xEB0A0A0C.toInt())
            setPadding(dp(12), dp(12), dp(12), dp(18))
            isClickable = true
            isFocusable = true
        }
        statusTitleView = TextView(this).apply {
            textSize = 13.5f
            setTypeface(null, Typeface.BOLD)
            setTextColor(WearDeckHelper.CHROME_WHITE_ARGB)
            gravity = Gravity.CENTER
        }
        statusDetailView = TextView(this).apply {
            textSize = 11f
            setTextColor(0xFF94A3B8.toInt())
            gravity = Gravity.CENTER
            setPadding(0, dp(4), 0, dp(10))
        }
        statusRetryBtn = createActionPill(
            label = "Retry",
            bgArgb = WearDeckHelper.SKY_BLUE_ARGB,
            fgArgb = WearDeckHelper.OBSIDIAN_SLATE_ARGB,
        ) {
            reconnect()
        }
        statusOverlay.addView(statusTitleView)
        statusOverlay.addView(statusDetailView)
        statusOverlay.addView(statusRetryBtn)
        contentStack.addView(statusOverlay)

        mainColumn.addView(contentStack)
        root.addView(
            mainColumn,
            FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            ),
        )
        return root
    }

    private fun switchTab(tab: Tab) {
        if (currentTab == tab) return
        currentTab = tab
        renderAll()
    }

    private fun renderAll() {
        renderTabs()
        renderHeaderStrip()
        renderVisibilityAndOverlay()
        renderKeysView()
        renderDialsView()
        renderMenuView()
        syncShimmerAnimator()
    }

    private fun renderTabs() {
        styleTabPill(tabKeysBtn, selected = currentTab == Tab.KEYS)
        styleTabPill(tabDialsBtn, selected = currentTab == Tab.DIALS)
        styleTabPill(tabMenuBtn, selected = currentTab == Tab.MENU)
    }

    private fun renderHeaderStrip() {
        val snap = latestSnapshot
        if (snap == null) {
            headerStripView.text = statusLabel(latestStatus.state)
            return
        }
        val dot = when (latestStatus.state) {
            WatchStatusMessage.STATE_LIVE -> "●"
            WatchStatusMessage.STATE_CONNECTING -> "◐"
            else -> "○"
        }
        val pagePart = if (snap.pageCount > 1) " • ${snap.pageIndex + 1}/${snap.pageCount}" else ""
        val folderPart = if (snap.inFolder && snap.folderPath.isNotEmpty()) {
            " • ${snap.folderPath.last()}"
        } else {
            ""
        }
        headerStripView.text = "$dot ${snap.activeProfileName}$pagePart$folderPart"
    }

    private fun renderVisibilityAndOverlay() {
        keysScrollView.visibility = if (currentTab == Tab.KEYS) View.VISIBLE else View.GONE
        dialsScrollView.visibility = if (currentTab == Tab.DIALS) View.VISIBLE else View.GONE
        menuScrollView.visibility = if (currentTab == Tab.MENU) View.VISIBLE else View.GONE

        val showOverlay = currentTab != Tab.MENU &&
            (latestStatus.state != WatchStatusMessage.STATE_LIVE || latestSnapshot == null)
        statusOverlay.visibility = if (showOverlay) View.VISIBLE else View.GONE
        if (showOverlay) {
            statusTitleView.text = statusLabel(latestStatus.state)
            statusDetailView.text = when (latestStatus.state) {
                WatchStatusMessage.STATE_UNPAIRED ->
                    "Pair PoundControl on your phone first, then tap Retry."
                WatchStatusMessage.STATE_LIVE ->
                    "Waiting for deck snapshot..."
                else ->
                    latestStatus.detail.ifBlank { "Waiting for phone relay..." }
            }
            statusRetryBtn.visibility = if (latestStatus.state == WatchStatusMessage.STATE_CONNECTING) {
                View.GONE
            } else {
                View.VISIBLE
            }
        }
    }

    private fun renderKeysView() {
        activeGlowTiles.clear()
        keysContainer.removeAllViews()
        val snap = latestSnapshot ?: return

        if (snap.inFolder || snap.pageCount > 1) {
            val navRow = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER
                setPadding(0, dp(2), 0, dp(6))
            }
            if (snap.inFolder) {
                navRow.addView(
                    createNavPill("← Back") {
                        it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                        sendDeckCommand(DeckCommand.closeFolder())
                    },
                )
            }
            if (snap.pageCount > 1) {
                if (snap.inFolder) {
                    navRow.addView(View(this), LinearLayout.LayoutParams(dp(6), 1))
                }
                navRow.addView(
                    createNavPill("◀") {
                        it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                        sendDeckCommand(DeckCommand.prevPage())
                    },
                )
                navRow.addView(
                    TextView(this).apply {
                        text = "${snap.pageIndex + 1}/${snap.pageCount}"
                        textSize = 10.5f
                        setTextColor(0xFF94A3B8.toInt())
                        setPadding(dp(6), 0, dp(6), 0)
                    },
                )
                navRow.addView(
                    createNavPill("▶") {
                        it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                        sendDeckCommand(DeckCommand.nextPage())
                    },
                )
            }
            keysContainer.addView(navRow)
        }

        val useGrid = WearDeckHelper.shouldUseGridLayout(layoutMode, snap.cols, snap.rows)
        if (useGrid) {
            renderKeysGrid(snap)
        } else {
            renderKeysList(snap)
        }
    }

    private fun renderKeysGrid(snap: DeckSnapshot) {
        val gap = dp(7)
        if (snap.cols <= 3 && snap.rows <= 4) {
            for (r in 0 until snap.rows) {
                val rowLayout = LinearLayout(this).apply {
                    orientation = LinearLayout.HORIZONTAL
                    clipChildren = false
                    clipToPadding = false
                    layoutParams = LinearLayout.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT,
                        ViewGroup.LayoutParams.WRAP_CONTENT,
                    ).apply {
                        bottomMargin = gap
                    }
                }
                for (c in 0 until snap.cols) {
                    val slot = snap.slots["$r,$c"]
                    val card = createKeyGridTile(r, c, slot)
                    val lp = LinearLayout.LayoutParams(0, dp(54), 1f).apply {
                        if (c < snap.cols - 1) marginEnd = gap
                    }
                    rowLayout.addView(card, lp)
                }
                keysContainer.addView(rowLayout)
            }
            return
        }

        val activeItems = collectActiveSlots(snap)
        if (activeItems.isEmpty()) {
            keysContainer.addView(createEmptyKeysMessage("No keys on ${snap.pageName}"))
            return
        }
        val columns = 2
        var idx = 0
        while (idx < activeItems.size) {
            val rowLayout = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                clipChildren = false
                clipToPadding = false
                layoutParams = LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ).apply {
                    bottomMargin = gap
                }
            }
            for (col in 0 until columns) {
                val itemIdx = idx + col
                if (itemIdx < activeItems.size) {
                    val (r, c, slot) = activeItems[itemIdx]
                    val card = createKeyGridTile(r, c, slot)
                    val lp = LinearLayout.LayoutParams(0, dp(54), 1f).apply {
                        if (col < columns - 1) marginEnd = gap
                    }
                    rowLayout.addView(card, lp)
                } else {
                    val spacer = View(this)
                    rowLayout.addView(spacer, LinearLayout.LayoutParams(0, dp(54), 1f))
                }
            }
            keysContainer.addView(rowLayout)
            idx += columns
        }
    }

    private fun renderKeysList(snap: DeckSnapshot) {
        val activeItems = collectActiveSlots(snap)
        if (activeItems.isEmpty()) {
            keysContainer.addView(createEmptyKeysMessage("No keys on ${snap.pageName}"))
            return
        }
        val gap = dp(8)
        for ((r, c, slot) in activeItems) {
            val effectiveAccent = WearDeckHelper.parseHexArgbOrNull(slot.color) ?: WearDeckHelper.SKY_BLUE_ARGB
            val customBg = WearDeckHelper.parseHexArgbOrNull(slot.bgColor)
            val isActive = (slot.assigned || slot.autoBack) && slot.state > 0
            val effectiveFill = customBg ?: if (isActive) {
                WearDeckHelper.blendArgb(WearDeckHelper.SURFACE_1_ARGB, effectiveAccent, 0.16f)
            } else {
                WearDeckHelper.SURFACE_1_ARGB
            }
            val fg = WearDeckHelper.contrastForegroundArgb(effectiveFill)

            val rowCard = GlowTileView(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                minimumHeight = dp(44)
                setPadding(dp(18), dp(8), dp(10), dp(8))
                configure(
                    assignedOrAutoBack = true,
                    accentArgb = effectiveAccent,
                    customBgArgb = customBg,
                    active = isActive,
                    flash = slot.flash,
                    barPosition = GlowTileView.AccentBarPosition.LEFT,
                )
                layoutParams = LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                ).apply {
                    bottomMargin = gap
                }
            }
            if (isActive) {
                activeGlowTiles.add(rowCard)
            }

            val textCol = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            }
            val titleView = TextView(this).apply {
                text = slot.title.ifBlank { "Key ${r + 1},${c + 1}" }
                textSize = 12f
                setTypeface(null, Typeface.BOLD)
                setTextColor(fg)
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
            }
            textCol.addView(titleView)

            if (slot.subtitle.isNotBlank()) {
                val subView = TextView(this).apply {
                    text = slot.subtitle
                    textSize = 10f
                    setTextColor(resolveSubtitleColor(effectiveAccent, effectiveFill, fg))
                    maxLines = 1
                    ellipsize = TextUtils.TruncateAt.END
                }
                textCol.addView(subView)
            }
            rowCard.addView(textCol)

            if (slot.state > 0) {
                val badge = TextView(this).apply {
                    text = "●"
                    textSize = 10f
                    setTextColor(effectiveAccent)
                    setPadding(dp(6), 0, dp(2), 0)
                }
                rowCard.addView(badge)
            }

            bindKeyClick(rowCard, r, c, slot)
            keysContainer.addView(rowCard)
        }
    }

    private fun createKeyGridTile(row: Int, col: Int, slot: DeckSlot?): View {
        val activeSlot = slot != null && (slot.assigned || slot.autoBack)
        if (!activeSlot) {
            return GlowTileView(this).apply {
                configure(assignedOrAutoBack = false)
            }
        }
        val effectiveAccent = WearDeckHelper.parseHexArgbOrNull(slot.color) ?: WearDeckHelper.SKY_BLUE_ARGB
        val customBg = WearDeckHelper.parseHexArgbOrNull(slot.bgColor)
        val isActive = slot.state > 0
        val effectiveFill = customBg ?: if (isActive) {
            WearDeckHelper.blendArgb(WearDeckHelper.SURFACE_1_ARGB, effectiveAccent, 0.16f)
        } else {
            WearDeckHelper.SURFACE_1_ARGB
        }
        val fg = WearDeckHelper.contrastForegroundArgb(effectiveFill)

        val tile = GlowTileView(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setPadding(dp(6), dp(11), dp(6), dp(5))
            configure(
                assignedOrAutoBack = true,
                accentArgb = effectiveAccent,
                customBgArgb = customBg,
                active = isActive,
                flash = slot.flash,
                barPosition = GlowTileView.AccentBarPosition.TOP,
            )
        }
        if (isActive) {
            activeGlowTiles.add(tile)
        }

        val titleView = TextView(this).apply {
            text = slot.title.ifBlank { "Key" }
            textSize = 11.5f
            setTypeface(null, Typeface.BOLD)
            setTextColor(fg)
            gravity = Gravity.CENTER
            maxLines = if (slot.subtitle.isBlank()) 2 else 1
            ellipsize = TextUtils.TruncateAt.END
        }
        tile.addView(titleView)

        if (slot.subtitle.isNotBlank()) {
            val subView = TextView(this).apply {
                text = slot.subtitle
                textSize = 9.5f
                setTextColor(resolveSubtitleColor(effectiveAccent, effectiveFill, fg))
                gravity = Gravity.CENTER
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
            }
            tile.addView(subView)
        }

        bindKeyClick(tile, row, col, slot)
        return tile
    }

    private fun resolveSubtitleColor(accentArgb: Int, fillArgb: Int, fallbackFgArgb: Int): Int {
        val lumAccent = WearDeckHelper.relativeLuminance(accentArgb)
        val lumFill = WearDeckHelper.relativeLuminance(fillArgb)
        val contrast = (maxOf(lumAccent, lumFill) + 0.05) / (minOf(lumAccent, lumFill) + 0.05)
        return if (contrast >= 3.0) accentArgb else WearDeckHelper.withAlpha(fallbackFgArgb, 0xCC)
    }

    private fun syncShimmerAnimator() {
        val shouldAnimate = isWindowActive &&
            currentTab == Tab.KEYS &&
            latestStatus.state == WatchStatusMessage.STATE_LIVE &&
            latestSnapshot != null &&
            activeGlowTiles.isNotEmpty() &&
            ValueAnimator.areAnimatorsEnabled()
        if (!shouldAnimate) {
            shimmerAnimator?.cancel()
            shimmerAnimator = null
            for (tile in activeGlowTiles) {
                tile.setShimmerLevel(1f)
            }
            return
        }
        if (shimmerAnimator == null) {
            shimmerAnimator = ValueAnimator.ofFloat(0f, 1f).apply {
                duration = 1300L
                repeatMode = ValueAnimator.REVERSE
                repeatCount = ValueAnimator.INFINITE
                interpolator = AccelerateDecelerateInterpolator()
                addUpdateListener { anim ->
                    val level = anim.animatedValue as Float
                    for (tile in activeGlowTiles) {
                        tile.setShimmerLevel(level)
                    }
                }
                start()
            }
        }
    }

    private fun bindKeyClick(view: View, row: Int, col: Int, slot: DeckSlot) {
        view.isClickable = true
        view.isFocusable = true
        view.setOnClickListener {
            it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
            if (slot.autoBack) {
                sendDeckCommand(DeckCommand.closeFolder())
            } else {
                sendDeckCommand(DeckCommand.keyDown(row, col))
                mainHandler.postDelayed(
                    { sendDeckCommand(DeckCommand.keyUp(row, col)) },
                    KEY_RELEASE_DELAY_MS,
                )
            }
        }
    }

    private fun renderDialsView() {
        val dials = latestSnapshot?.dialSlots ?: emptyList()
        if (dials.isEmpty() || dials.none { it.assigned }) {
            dialsEmptyView.visibility = View.VISIBLE
            dialsActiveGroup.visibility = View.GONE
            return
        }
        dialsEmptyView.visibility = View.GONE
        dialsActiveGroup.visibility = View.VISIBLE

        val selected = currentSelectedDial()
        dialArcView.bind(selected, dialPressed)

        dialSwitcherRow.removeAllViews()
        val assignedDials = dials.filter { it.assigned }
        if (assignedDials.size > 1) {
            dialSwitcherRow.visibility = View.VISIBLE
            for (d in assignedDials) {
                val active = d.index == selected?.index
                val pill = TextView(this).apply {
                    text = "D${d.index + 1}"
                    textSize = 10f
                    setTypeface(null, if (active) Typeface.BOLD else Typeface.NORMAL)
                    setTextColor(
                        if (active) WearDeckHelper.OBSIDIAN_SLATE_ARGB else 0xFF94A3B8.toInt(),
                    )
                    setPadding(dp(8), dp(3), dp(8), dp(3))
                    background = GradientDrawable().apply {
                        cornerRadius = dp(10).toFloat()
                        setColor(if (active) WearDeckHelper.SKY_BLUE_ARGB else 0xFF161922.toInt())
                    }
                    layoutParams = LinearLayout.LayoutParams(
                        ViewGroup.LayoutParams.WRAP_CONTENT,
                        ViewGroup.LayoutParams.WRAP_CONTENT,
                    ).apply {
                        marginEnd = dp(4)
                    }
                    setOnClickListener {
                        it.performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
                        selectedDialIndex = d.index
                        renderDialsView()
                    }
                }
                dialSwitcherRow.addView(pill)
            }
        } else {
            dialSwitcherRow.visibility = View.GONE
        }

        val activeIdx = assignedDials.indexOfFirst { it.index == selected?.index }.coerceAtLeast(0)
        dialCounterView.text = if (assignedDials.size > 1) {
            "Dial ${activeIdx + 1}/${assignedDials.size}"
        } else {
            "Crown Ready"
        }
    }

    private fun renderMenuView() {
        menuContainer.removeAllViews()
        val snap = latestSnapshot

        menuContainer.addView(createSectionHeader("Connection"))
        val statusCard = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(10), dp(8), dp(10), dp(8))
            background = GradientDrawable().apply {
                cornerRadius = dp(12).toFloat()
                setColor(0xFF14161D.toInt())
                setStroke(dp(1), 0xFF272B36.toInt())
            }
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply {
                bottomMargin = dp(8)
            }
        }
        statusCard.addView(
            TextView(this).apply {
                text = "${statusLabel(latestStatus.state)} • ${latestStatus.detail}"
                textSize = 11f
                setTextColor(WearDeckHelper.CHROME_WHITE_ARGB)
            },
        )
        val reconnectBtn = createActionPill(
            label = "Reconnect Relay",
            bgArgb = 0xFF1E293B.toInt(),
            fgArgb = WearDeckHelper.SKY_BLUE_ARGB,
        ) {
            reconnect()
        }.apply {
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply {
                topMargin = dp(6)
            }
        }
        statusCard.addView(reconnectBtn)
        menuContainer.addView(statusCard)

        menuContainer.addView(createSectionHeader("Watch Profile"))
        if (snap == null || snap.profiles.isEmpty()) {
            menuContainer.addView(createEmptyKeysMessage("Connect to load profiles"))
        } else {
            for (prof in snap.profiles) {
                val active = prof.id == snap.activeProfileId
                val row = createChoiceRow(
                    label = prof.name,
                    badge = if (active) "Active" else "",
                    selected = active,
                ) {
                    it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                    sendDeckCommand(DeckCommand.switchProfile(prof.id))
                    switchTab(Tab.KEYS)
                }
                menuContainer.addView(row)
            }
        }

        menuContainer.addView(createSectionHeader("Key Layout"))
        for (mode in LayoutMode.entries) {
            val active = layoutMode == mode
            val row = createChoiceRow(
                label = mode.label,
                badge = if (active) "●" else "",
                selected = active,
            ) {
                it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                layoutMode = mode
                prefs.edit().putString(KEY_LAYOUT_MODE, mode.storageKey).apply()
                renderKeysView()
                renderMenuView()
            }
            menuContainer.addView(row)
        }

        menuContainer.addView(createSectionHeader("Display"))
        menuContainer.addView(
            createChoiceRow(
                label = "Keep Screen On",
                badge = if (keepScreenOn) "ON" else "OFF",
                selected = keepScreenOn,
            ) {
                it.performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                keepScreenOn = !keepScreenOn
                prefs.edit().putBoolean(KEY_KEEP_SCREEN_ON, keepScreenOn).apply()
                applyKeepScreenOnFlag()
                renderMenuView()
            },
        )
    }

    private fun collectActiveSlots(snap: DeckSnapshot): List<Triple<Int, Int, DeckSlot>> {
        val out = ArrayList<Triple<Int, Int, DeckSlot>>()
        for (r in 0 until snap.rows) {
            for (c in 0 until snap.cols) {
                val slot = snap.slots["$r,$c"] ?: continue
                if (slot.assigned || slot.autoBack) {
                    out.add(Triple(r, c, slot))
                }
            }
        }
        return out
    }

    private fun statusLabel(state: String): String = when (state) {
        WatchStatusMessage.STATE_LIVE -> "Connected"
        WatchStatusMessage.STATE_CONNECTING -> "Connecting..."
        WatchStatusMessage.STATE_UNPAIRED -> "Phone Not Paired"
        WatchStatusMessage.STATE_OFFLINE -> "Host Offline"
        WatchStatusMessage.STATE_ERROR -> "Connection Error"
        else -> state
    }

    private fun createTabPill(label: String, onClick: () -> Unit): TextView {
        return TextView(this).apply {
            text = label
            textSize = 11f
            gravity = Gravity.CENTER
            setPadding(dp(10), dp(4), dp(10), dp(4))
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply {
                marginEnd = dp(4)
            }
            setOnClickListener {
                it.performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
                onClick()
            }
        }
    }

    private fun styleTabPill(pill: TextView, selected: Boolean) {
        pill.setTypeface(null, if (selected) Typeface.BOLD else Typeface.NORMAL)
        pill.setTextColor(
            if (selected) WearDeckHelper.OBSIDIAN_SLATE_ARGB else 0xFF94A3B8.toInt(),
        )
        pill.background = GradientDrawable().apply {
            cornerRadius = dp(14).toFloat()
            setColor(if (selected) WearDeckHelper.SKY_BLUE_ARGB else 0xFF14161D.toInt())
        }
    }

    private fun createNavPill(label: String, onClick: (View) -> Unit): TextView {
        return createActionPill(
            label = label,
            bgArgb = 0xFF161922.toInt(),
            fgArgb = WearDeckHelper.CHROME_WHITE_ARGB,
            onClick = onClick,
        )
    }

    private fun createActionPill(
        label: String,
        bgArgb: Int,
        fgArgb: Int,
        onClick: (View) -> Unit,
    ): TextView {
        return TextView(this).apply {
            text = label
            textSize = 11.5f
            setTypeface(null, Typeface.BOLD)
            setTextColor(fgArgb)
            gravity = Gravity.CENTER
            setPadding(dp(12), dp(6), dp(12), dp(6))
            background = createCardBackground(bgArgb, 0xFF272B36.toInt(), dp(1), dp(14).toFloat())
            setOnClickListener(onClick)
        }
    }

    private fun createSectionHeader(title: String): TextView {
        return TextView(this).apply {
            text = title.uppercase()
            textSize = 9.5f
            setTypeface(null, Typeface.BOLD)
            setTextColor(0xFF64748B.toInt())
            setPadding(dp(2), dp(6), dp(2), dp(4))
        }
    }

    private fun createChoiceRow(
        label: String,
        badge: String,
        selected: Boolean,
        onClick: (View) -> Unit,
    ): View {
        val bg = if (selected) 0xFF162230.toInt() else 0xFF14161D.toInt()
        val border = if (selected) WearDeckHelper.SKY_BLUE_ARGB else 0xFF272B36.toInt()
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(10), dp(8), dp(10), dp(8))
            background = createCardBackground(bg, border, dp(1), dp(10).toFloat())
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply {
                bottomMargin = dp(5)
            }
            setOnClickListener(onClick)
        }
        row.addView(
            TextView(this).apply {
                text = label
                textSize = 11.5f
                setTypeface(null, if (selected) Typeface.BOLD else Typeface.NORMAL)
                setTextColor(WearDeckHelper.CHROME_WHITE_ARGB)
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            },
        )
        if (badge.isNotEmpty()) {
            row.addView(
                TextView(this).apply {
                    text = badge
                    textSize = 10f
                    setTypeface(null, Typeface.BOLD)
                    setTextColor(if (selected) WearDeckHelper.SKY_BLUE_ARGB else 0xFF64748B.toInt())
                    setPadding(dp(6), 0, 0, 0)
                },
            )
        }
        return row
    }

    private fun createEmptyKeysMessage(message: String): TextView {
        return TextView(this).apply {
            text = message
            textSize = 11.5f
            setTextColor(0xFF64748B.toInt())
            gravity = Gravity.CENTER
            setPadding(dp(8), dp(16), dp(8), dp(16))
        }
    }

    private fun createCardBackground(
        bgArgb: Int,
        strokeArgb: Int,
        strokePx: Int,
        cornerRadiusPx: Float,
    ): RippleDrawable {
        val shape = GradientDrawable().apply {
            cornerRadius = cornerRadiusPx
            setColor(bgArgb)
            setStroke(strokePx, strokeArgb)
        }
        val rippleColor = ColorStateList.valueOf(withAlpha(WearDeckHelper.SKY_BLUE_ARGB, 0x44))
        return RippleDrawable(rippleColor, shape, shape)
    }

    private fun withAlpha(argb: Int, alpha: Int): Int {
        return (alpha shl 24) or (argb and 0x00FFFFFF)
    }

    private fun dp(value: Int): Int {
        return (value * resources.displayMetrics.density).toInt().coerceAtLeast(1)
    }
}

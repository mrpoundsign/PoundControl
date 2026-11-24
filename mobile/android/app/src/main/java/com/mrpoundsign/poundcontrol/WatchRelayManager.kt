package com.mrpoundsign.poundcontrol

import android.content.Context
import com.google.android.gms.wearable.Wearable
import com.mrpoundsign.poundcontrol.protocol.AuthRecord
import com.mrpoundsign.poundcontrol.protocol.DeckCommand
import com.mrpoundsign.poundcontrol.protocol.DeckSnapshot
import com.mrpoundsign.poundcontrol.protocol.WatchStatusMessage
import com.mrpoundsign.poundcontrol.protocol.WearProtocol
import com.mrpoundsign.poundcontrol.protocol.WebDeckSocketClient
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Bridges Wear OS watch sessions (Bluetooth Wear Data Layer) to an ephemeral, independent
 * WebSocket viewport on the PoundControl PC host using the phone's paired credentials.
 */
object WatchRelayManager {
    private const val PREFS_NAME = "poundcontrol_prefs"
    private const val AUTH_PREFS_NAME = "poundcontrol_auth"
    private const val RELAY_PREFS_NAME = "poundcontrol_watch_relay"
    private const val KEY_BASE_URL = "last_base_url"
    private const val KEY_WATCH_PROFILE_ID = "watch_profile_id"
    private const val IDLE_CLOSE_DELAY_MS = 15_000L

    private val lock = Any()
    private val idleScheduler = Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "PoundControl-WatchRelay").apply { isDaemon = true }
    }

    private var appContext: Context? = null
    private var activeWatchNodeId: String? = null
    private var socketClient: WebDeckSocketClient? = null
    private var connectedOrigin: String? = null
    private var connectedDeviceId: String? = null
    private var lastSnapshot: DeckSnapshot? = null
    private var lastStatus: WatchStatusMessage? = null
    private var pendingCommand: String? = null
    private var idleCloseFuture: ScheduledFuture<*>? = null
    private var autoPickedWatchProfile = false

    fun handleWatchMessage(context: Context, nodeId: String, path: String, data: ByteArray) {
        val ctx = context.applicationContext
        synchronized(lock) {
            appContext = ctx
            if (nodeId.isNotBlank()) {
                activeWatchNodeId = nodeId
            }
            cancelIdleCloseLocked()
        }

        when (path) {
            WearProtocol.PATH_OPEN -> {
                val client = ensureConnected(ctx, nodeId) ?: return
                val cachedSnap: DeckSnapshot?
                val cachedStatus: WatchStatusMessage?
                synchronized(lock) {
                    cachedSnap = if (client.isConnected()) lastSnapshot else null
                    cachedStatus = lastStatus
                }
                if (cachedStatus != null) {
                    sendToWatch(ctx, nodeId, WearProtocol.PATH_STATUS, cachedStatus.toJson())
                }
                if (cachedSnap != null) {
                    sendToWatch(ctx, nodeId, WearProtocol.PATH_SNAPSHOT, cachedSnap.toCompactWatchJson())
                }
            }

            WearProtocol.PATH_PING -> {
                ensureConnected(ctx, nodeId)
            }

            WearProtocol.PATH_CLOSE -> {
                scheduleIdleClose()
            }

            WearProtocol.PATH_CMD -> {
                val rawCmd = data.toString(Charsets.UTF_8).trim()
                if (rawCmd.isEmpty()) return
                persistSwitchProfileIfPresent(ctx, rawCmd)
                val client = ensureConnected(ctx, nodeId) ?: return
                if (!client.sendCommand(rawCmd)) {
                    synchronized(lock) {
                        pendingCommand = rawCmd
                    }
                }
            }
        }
    }

    private fun persistSwitchProfileIfPresent(ctx: Context, rawCmd: String) {
        try {
            val obj = JSONObject(rawCmd)
            if (obj.optString("type") == "switchProfile") {
                val profId = obj.optString("profileId").trim()
                if (profId.isNotEmpty()) {
                    ctx.getSharedPreferences(RELAY_PREFS_NAME, Context.MODE_PRIVATE)
                        .edit()
                        .putString(KEY_WATCH_PROFILE_ID, profId)
                        .apply()
                }
            }
        } catch (_: Exception) {
            // Ignore malformed command JSON; WebDeckSocketClient / Go server handles validation.
        }
    }

    private fun ensureConnected(ctx: Context, replyNodeId: String): WebDeckSocketClient? {
        val savedBase = ctx.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .getString(KEY_BASE_URL, null)
        val baseOrigin = savedBase?.let { DeckUrlValidator.baseOriginUrl(it) }
        val authJson = if (baseOrigin != null) {
            ctx.getSharedPreferences(AUTH_PREFS_NAME, Context.MODE_PRIVATE).getString(baseOrigin, null)
        } else {
            null
        }
        val auth = authJson?.let { AuthRecord.fromJson(it) }

        if (baseOrigin == null || auth == null) {
            disconnectClient()
            val msg = WatchStatusMessage(
                WatchStatusMessage.STATE_UNPAIRED,
                "Open PoundControl on your phone to connect and pair with your PC.",
            )
            synchronized(lock) {
                lastStatus = msg
                lastSnapshot = null
            }
            sendToWatch(ctx, replyNodeId, WearProtocol.PATH_STATUS, msg.toJson())
            return null
        }

        val existing: WebDeckSocketClient?
        synchronized(lock) {
            if (socketClient != null && connectedOrigin == baseOrigin && connectedDeviceId == auth.deviceId) {
                return socketClient
            }
            existing = socketClient
            socketClient = null
            connectedOrigin = baseOrigin
            connectedDeviceId = auth.deviceId
            lastSnapshot = null
            autoPickedWatchProfile = false
        }
        existing?.shutdown()

        val client = WebDeckSocketClient(
            baseOrigin = baseOrigin,
            auth = auth,
            listener = object : WebDeckSocketClient.Listener {
                override fun onConnected() {
                    val savedProfileId = ctx.getSharedPreferences(RELAY_PREFS_NAME, Context.MODE_PRIVATE)
                        .getString(KEY_WATCH_PROFILE_ID, "")
                        .orEmpty()
                    val curClient: WebDeckSocketClient?
                    val pending: String?
                    synchronized(lock) {
                        curClient = socketClient
                        pending = pendingCommand
                        pendingCommand = null
                    }
                    curClient?.sendCommand(DeckCommand.configureSession(ephemeral = true, profileId = savedProfileId))
                    if (!pending.isNullOrBlank()) {
                        curClient?.sendCommand(pending)
                    }
                }

                override fun onSnapshot(snapshot: DeckSnapshot) {
                    val relayPrefs = ctx.getSharedPreferences(RELAY_PREFS_NAME, Context.MODE_PRIVATE)
                    val savedProfileId = relayPrefs.getString(KEY_WATCH_PROFILE_ID, "").orEmpty()
                    var shouldAutoSwitch = false
                    var watchProfileId = ""
                    synchronized(lock) {
                        if (savedProfileId.isEmpty() && !autoPickedWatchProfile) {
                            autoPickedWatchProfile = true
                            val watchProf = snapshot.profiles.firstOrNull { it.name.equals("Watch", ignoreCase = true) }
                            if (watchProf != null) {
                                watchProfileId = watchProf.id
                                shouldAutoSwitch = true
                            }
                        }
                        lastSnapshot = snapshot
                    }
                    if (shouldAutoSwitch && watchProfileId.isNotEmpty()) {
                        relayPrefs.edit().putString(KEY_WATCH_PROFILE_ID, watchProfileId).apply()
                        if (snapshot.activeProfileId != watchProfileId) {
                            socketClient?.sendCommand(DeckCommand.switchProfile(watchProfileId))
                        }
                    }
                    val targetNode = synchronized(lock) { activeWatchNodeId }
                    if (!targetNode.isNullOrBlank()) {
                        sendToWatch(ctx, targetNode, WearProtocol.PATH_SNAPSHOT, snapshot.toCompactWatchJson())
                    }
                }

                override fun onStatusChanged(state: String, detail: String) {
                    val status = WatchStatusMessage(state, detail)
                    val targetNode: String?
                    synchronized(lock) {
                        lastStatus = status
                        targetNode = activeWatchNodeId
                    }
                    if (!targetNode.isNullOrBlank()) {
                        sendToWatch(ctx, targetNode, WearProtocol.PATH_STATUS, status.toJson())
                    }
                }

                override fun onAuthRevoked(reason: String) {
                    val status = WatchStatusMessage(WatchStatusMessage.STATE_UNPAIRED, reason)
                    val targetNode: String?
                    val oldClient: WebDeckSocketClient?
                    synchronized(lock) {
                        lastStatus = status
                        lastSnapshot = null
                        oldClient = socketClient
                        socketClient = null
                        connectedOrigin = null
                        connectedDeviceId = null
                        targetNode = activeWatchNodeId
                    }
                    oldClient?.shutdown()
                    if (!targetNode.isNullOrBlank()) {
                        sendToWatch(ctx, targetNode, WearProtocol.PATH_STATUS, status.toJson())
                    }
                }
            },
        )
        synchronized(lock) {
            socketClient = client
        }
        client.connect()
        return client
    }

    private fun scheduleIdleClose() {
        synchronized(lock) {
            cancelIdleCloseLocked()
            idleCloseFuture = idleScheduler.schedule(
                { disconnectClient() },
                IDLE_CLOSE_DELAY_MS,
                TimeUnit.MILLISECONDS,
            )
        }
    }

    private fun cancelIdleCloseLocked() {
        idleCloseFuture?.cancel(false)
        idleCloseFuture = null
    }

    private fun disconnectClient() {
        val toStop: WebDeckSocketClient?
        synchronized(lock) {
            cancelIdleCloseLocked()
            toStop = socketClient
            socketClient = null
            connectedOrigin = null
            connectedDeviceId = null
            lastSnapshot = null
            pendingCommand = null
        }
        toStop?.shutdown()
    }

    private fun sendToWatch(ctx: Context, nodeId: String, path: String, jsonPayload: String) {
        if (nodeId.isBlank()) return
        Wearable.getMessageClient(ctx)
            .sendMessage(nodeId, path, jsonPayload.toByteArray(Charsets.UTF_8))
    }
}

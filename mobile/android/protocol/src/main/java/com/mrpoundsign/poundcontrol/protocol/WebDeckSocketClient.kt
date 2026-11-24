package com.mrpoundsign.poundcontrol.protocol

import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/**
 * Mutually authenticated WebSocket client implementing PoundControl's WS v2 protocol
 * (`auth_hello` / `auth_response` / `auth_ok` + monotonic HMAC-SHA256 signed frames).
 */
class WebDeckSocketClient(
    private val baseOrigin: String,
    private val auth: AuthRecord,
    private val listener: Listener,
    private val httpClient: OkHttpClient = defaultHttpClient(),
) {
    interface Listener {
        fun onConnected()
        fun onSnapshot(snapshot: DeckSnapshot)
        fun onStatusChanged(state: String, detail: String)
        fun onAuthRevoked(reason: String)
    }

    private data class PendingHandshake(
        val t1: ByteArray,
        val hostPub: ByteArray,
        val ephPriv: ByteArray,
    )

    private data class ActiveSession(
        val clientMacKey: ByteArray,
        val serverMacKey: ByteArray,
        var txSeq: Long = 0L,
        var rxSeq: Long = 0L,
    )

    private val lock = Any()
    private val scheduler: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "PoundControl-WS").apply { isDaemon = true }
    }

    private var active = false
    private var ws: WebSocket? = null
    private var pendingHandshake: PendingHandshake? = null
    private var session: ActiveSession? = null
    private var reconnectDelayMs = 600L
    private var reconnectFuture: ScheduledFuture<*>? = null
    private var heartbeatFuture: ScheduledFuture<*>? = null

    fun connect() {
        synchronized(lock) {
            if (active && ws != null) return
            active = true
            reconnectDelayMs = 600L
            cancelReconnectLocked()
            openSocketLocked()
        }
    }

    fun disconnect() {
        val toClose: WebSocket?
        synchronized(lock) {
            active = false
            cancelReconnectLocked()
            cancelHeartbeatLocked()
            pendingHandshake = null
            session = null
            toClose = ws
            ws = null
        }
        toClose?.close(1000, "Client closed")
    }

    fun shutdown() {
        disconnect()
        scheduler.shutdownNow()
    }

    fun isConnected(): Boolean = synchronized(lock) { ws != null && session != null }

    fun sendCommand(jsonPayload: String): Boolean {
        val currentWs: WebSocket
        val frameJson: String
        synchronized(lock) {
            val curWs = ws ?: return false
            val curSess = session ?: return false
            curSess.txSeq += 1L
            val seq = curSess.txSeq
            val mac = WebDeckCrypto.computeFrameMAC(curSess.clientMacKey, "c2s", seq, jsonPayload)
            frameJson = JSONObject()
                .put("seq", seq)
                .put("payload", jsonPayload)
                .put("mac", WebDeckCrypto.toBase64(mac))
                .toString()
            currentWs = curWs
        }
        return currentWs.send(frameJson)
    }

    private fun openSocketLocked() {
        if (!active) return
        ws?.cancel()
        pendingHandshake = null
        session = null
        cancelHeartbeatLocked()

        val wsUrl = toWebSocketUrl(baseOrigin)
        val originHeader = baseOrigin.trim().removeSuffix("/")
        val request = Request.Builder()
            .url(wsUrl)
            .header("Origin", originHeader)
            .build()

        listener.onStatusChanged(WatchStatusMessage.STATE_CONNECTING, "Connecting to PC...")
        var createdSocket: WebSocket? = null
        createdSocket = httpClient.newWebSocket(
            request,
            object : WebSocketListener() {
                override fun onMessage(webSocket: WebSocket, text: String) {
                    if (!isCurrentSocket(webSocket)) return
                    handleIncomingMessage(webSocket, text)
                }

                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.close(1000, null)
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    handleSocketDropped(webSocket, if (reason.isNotBlank()) reason else "Disconnected from PC")
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    handleSocketDropped(webSocket, t.message ?: "Unable to reach PC")
                }
            },
        )
        ws = createdSocket
    }

    private fun isCurrentSocket(webSocket: WebSocket): Boolean = synchronized(lock) { ws === webSocket }

    private fun handleIncomingMessage(webSocket: WebSocket, rawText: String) {
        val msg = try {
            JSONObject(rawText)
        } catch (_: Exception) {
            return
        }

        val currentSess = synchronized(lock) { session }
        if (currentSess == null) {
            handleHandshakeMessage(webSocket, msg)
            return
        }

        val seq = msg.optLong("seq", -1L)
        val payload = msg.optString("payload", "")
        val macB64 = msg.optString("mac", "")
        if (seq <= 0L || payload.isEmpty() || macB64.isEmpty()) {
            abortSecurityFailure(webSocket, "Invalid signed host frame")
            return
        }

        val valid = synchronized(lock) {
            val cur = session
            if (cur == null || ws !== webSocket) return
            if (seq <= cur.rxSeq) {
                false
            } else {
                val expectedMac = WebDeckCrypto.computeFrameMAC(cur.serverMacKey, "s2c", seq, payload)
                val actualMac = try {
                    WebDeckCrypto.fromBase64(macB64)
                } catch (_: Exception) {
                    ByteArray(0)
                }
                if (WebDeckCrypto.constantTimeEquals(actualMac, expectedMac)) {
                    cur.rxSeq = seq
                    true
                } else {
                    false
                }
            }
        }

        if (!valid) {
            abortSecurityFailure(webSocket, "Host frame authentication failed")
            return
        }

        val inner = try {
            JSONObject(payload)
        } catch (_: Exception) {
            return
        }
        when (inner.optString("type")) {
            "snapshot" -> {
                val snapObj = inner.optJSONObject("snapshot") ?: return
                val snap = DeckSnapshot.fromJson(snapObj)
                listener.onSnapshot(snap)
            }

            "auth_revoked" -> {
                val reason = inner.optString("message", "Device access revoked on host")
                revokeAndStop(webSocket, reason)
            }

            "error" -> {
                val errMsg = inner.optString("message", "")
                if (errMsg.isNotBlank()) {
                    listener.onStatusChanged(WatchStatusMessage.STATE_ERROR, errMsg)
                }
            }
        }
    }

    private fun handleHandshakeMessage(webSocket: WebSocket, msg: JSONObject) {
        when (msg.optString("type")) {
            "auth_error" -> {
                val errMsg = msg.optString("message", "Authentication failed")
                if (errMsg.contains("not paired", ignoreCase = true) || errMsg.contains("revoked", ignoreCase = true)) {
                    revokeAndStop(webSocket, errMsg)
                } else {
                    synchronized(lock) {
                        if (ws === webSocket) {
                            ws = null
                            pendingHandshake = null
                        }
                    }
                    webSocket.close(1008, errMsg)
                    listener.onStatusChanged(WatchStatusMessage.STATE_ERROR, errMsg)
                    scheduleReconnect()
                }
            }

            "auth_hello" -> {
                if (msg.optString("hostId") != auth.hostId || msg.optString("hostPublicKey") != auth.hostPublicKey) {
                    revokeAndStop(webSocket, "Host identity mismatch")
                    return
                }
                try {
                    val hostPub = WebDeckCrypto.fromBase64(auth.hostPublicKey)
                    val clientPub = WebDeckCrypto.fromBase64(auth.clientPublicKey)
                    val clientSeed = WebDeckCrypto.fromBase64(auth.clientSeed)
                    val serverNonce = WebDeckCrypto.fromBase64(msg.optString("serverNonce"))
                    val clientNonce = WebDeckCrypto.randomBytes(32)
                    val eph = WebDeckCrypto.x25519GenerateKeypair()
                    val t1 = WebDeckCrypto.buildWSTranscript1(
                        hostId = auth.hostId,
                        deviceId = auth.deviceId,
                        hostPub = hostPub,
                        clientPub = clientPub,
                        serverNonce = serverNonce,
                        clientNonce = clientNonce,
                        clientEphPub = eph.publicKey,
                    )
                    val clientSig = WebDeckCrypto.ed25519Sign(clientSeed, t1)
                    synchronized(lock) {
                        if (ws !== webSocket) return
                        pendingHandshake = PendingHandshake(t1, hostPub, eph.privateKey)
                    }
                    val resp = JSONObject()
                        .put("type", "auth_response")
                        .put("deviceId", auth.deviceId)
                        .put("clientNonce", WebDeckCrypto.toBase64(clientNonce))
                        .put("clientEphPub", WebDeckCrypto.toBase64(eph.publicKey))
                        .put("clientSig", WebDeckCrypto.toBase64(clientSig))
                        .toString()
                    webSocket.send(resp)
                } catch (_: Exception) {
                    abortSecurityFailure(webSocket, "Client cryptographic identity error")
                }
            }

            "auth_ok" -> {
                val pending = synchronized(lock) { pendingHandshake } ?: return
                try {
                    val serverEphPub = WebDeckCrypto.fromBase64(msg.optString("serverEphPub"))
                    val hostSig = WebDeckCrypto.fromBase64(msg.optString("hostSig"))
                    val t2 = WebDeckCrypto.buildWSTranscript2(pending.t1, serverEphPub)
                    if (!WebDeckCrypto.ed25519Verify(pending.hostPub, t2, hostSig)) {
                        abortSecurityFailure(webSocket, "Host authentication signature failed")
                        return
                    }
                    val shared = WebDeckCrypto.x25519SharedSecret(pending.ephPriv, serverEphPub)
                    val keys = WebDeckCrypto.deriveWSSessionKeys(shared, t2)
                    synchronized(lock) {
                        if (ws !== webSocket) return
                        pendingHandshake = null
                        session = ActiveSession(keys.clientMacKey, keys.serverMacKey)
                        reconnectDelayMs = 600L
                        startHeartbeatLocked()
                    }
                    listener.onStatusChanged(WatchStatusMessage.STATE_LIVE, "Connected")
                    listener.onConnected()
                } catch (_: Exception) {
                    abortSecurityFailure(webSocket, "Host key exchange failed")
                }
            }
        }
    }

    private fun revokeAndStop(webSocket: WebSocket, reason: String) {
        synchronized(lock) {
            active = false
            cancelReconnectLocked()
            cancelHeartbeatLocked()
            pendingHandshake = null
            session = null
            if (ws === webSocket) ws = null
        }
        webSocket.close(1008, reason)
        listener.onAuthRevoked(reason)
    }

    private fun abortSecurityFailure(webSocket: WebSocket, reason: String) {
        synchronized(lock) {
            active = false
            cancelReconnectLocked()
            cancelHeartbeatLocked()
            pendingHandshake = null
            session = null
            if (ws === webSocket) ws = null
        }
        webSocket.close(1008, reason)
        listener.onStatusChanged(WatchStatusMessage.STATE_ERROR, reason)
    }

    private fun handleSocketDropped(webSocket: WebSocket, detail: String) {
        val shouldRetry: Boolean
        synchronized(lock) {
            if (ws !== webSocket) return
            ws = null
            pendingHandshake = null
            session = null
            cancelHeartbeatLocked()
            shouldRetry = active
        }
        if (shouldRetry) {
            listener.onStatusChanged(WatchStatusMessage.STATE_OFFLINE, detail)
            scheduleReconnect()
        }
    }

    private fun scheduleReconnect() {
        synchronized(lock) {
            if (!active || reconnectFuture != null || scheduler.isShutdown) return
            val delay = reconnectDelayMs
            reconnectDelayMs = (reconnectDelayMs * 3L / 2L).coerceAtMost(5000L)
            reconnectFuture = scheduler.schedule(
                {
                    synchronized(lock) {
                        reconnectFuture = null
                        if (active && ws == null) {
                            openSocketLocked()
                        }
                    }
                },
                delay,
                TimeUnit.MILLISECONDS,
            )
        }
    }

    private fun startHeartbeatLocked() {
        cancelHeartbeatLocked()
        if (scheduler.isShutdown) return
        heartbeatFuture = scheduler.scheduleAtFixedRate(
            {
                if (isConnected()) {
                    sendCommand(DeckCommand.ping())
                }
            },
            15L,
            15L,
            TimeUnit.SECONDS,
        )
    }

    private fun cancelReconnectLocked() {
        reconnectFuture?.cancel(false)
        reconnectFuture = null
    }

    private fun cancelHeartbeatLocked() {
        heartbeatFuture?.cancel(false)
        heartbeatFuture = null
    }

    companion object {
        fun defaultHttpClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(5, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .build()

        fun toWebSocketUrl(baseOrigin: String): String {
            val trimmed = baseOrigin.trim().removeSuffix("/")
            return when {
                trimmed.startsWith("https://", ignoreCase = true) ->
                    "wss://" + trimmed.substring("https://".length) + "/ws"

                trimmed.startsWith("http://", ignoreCase = true) ->
                    "ws://" + trimmed.substring("http://".length) + "/ws"

                else -> "ws://$trimmed/ws"
            }
        }
    }
}

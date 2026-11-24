package com.mrpoundsign.poundcontrol.protocol

import org.json.JSONArray
import org.json.JSONObject

data class AuthRecord(
    val hostId: String,
    val hostName: String,
    val hostPublicKey: String,
    val deviceId: String,
    val deviceName: String,
    val clientSeed: String,
    val clientPublicKey: String,
) {
    companion object {
        fun fromJson(raw: String): AuthRecord? {
            if (raw.isBlank()) return null
            return try {
                val obj = JSONObject(raw)
                val hostId = obj.optString("hostId").trim()
                val hostPub = obj.optString("hostPublicKey").trim()
                val deviceId = obj.optString("deviceId").trim()
                val clientSeed = obj.optString("clientSeed").trim()
                val clientPub = obj.optString("clientPublicKey").trim()
                if (hostId.isEmpty() || hostPub.isEmpty() || deviceId.isEmpty() || clientSeed.isEmpty() || clientPub.isEmpty()) {
                    return null
                }
                AuthRecord(
                    hostId = hostId,
                    hostName = obj.optString("hostName").trim(),
                    hostPublicKey = hostPub,
                    deviceId = deviceId,
                    deviceName = obj.optString("deviceName").trim(),
                    clientSeed = clientSeed,
                    clientPublicKey = clientPub,
                )
            } catch (_: Exception) {
                null
            }
        }
    }
}

data class DeckProfile(
    val id: String,
    val name: String,
)

data class DeckSlot(
    val assigned: Boolean,
    val autoBack: Boolean = false,
    val title: String = "",
    val subtitle: String = "",
    val color: String = "",
    val bgColor: String = "",
    val state: Int = 0,
    val flash: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("assigned", assigned)
        if (autoBack) put("autoBack", true)
        put("title", title)
        if (subtitle.isNotEmpty()) put("subtitle", subtitle)
        if (color.isNotEmpty()) put("color", color)
        if (bgColor.isNotEmpty()) put("bgColor", bgColor)
        if (state != 0) put("state", state)
        if (flash.isNotEmpty()) put("flash", flash)
    }

    companion object {
        fun fromJson(obj: JSONObject): DeckSlot = DeckSlot(
            assigned = obj.optBoolean("assigned", false),
            autoBack = obj.optBoolean("autoBack", false),
            title = obj.optString("title", ""),
            subtitle = obj.optString("subtitle", ""),
            color = obj.optString("color", ""),
            bgColor = obj.optString("bgColor", ""),
            state = obj.optInt("state", 0),
            flash = obj.optString("flash", ""),
        )
    }
}

data class DeckDial(
    val index: Int,
    val assigned: Boolean,
    val title: String,
    val value: String,
    val color: String = "",
    val bgColor: String = "",
    val hasIndicator: Boolean = false,
    val indicatorPct: Double = 0.0,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("index", index)
        put("assigned", assigned)
        put("title", title)
        put("value", value)
        if (color.isNotEmpty()) put("color", color)
        if (bgColor.isNotEmpty()) put("bgColor", bgColor)
        if (hasIndicator) {
            put("hasIndicator", true)
            put("indicatorPct", indicatorPct)
        }
    }

    companion object {
        fun fromJson(obj: JSONObject): DeckDial = DeckDial(
            index = obj.optInt("index", 0),
            assigned = obj.optBoolean("assigned", false),
            title = obj.optString("title", ""),
            value = obj.optString("value", ""),
            color = obj.optString("color", ""),
            bgColor = obj.optString("bgColor", ""),
            hasIndicator = obj.optBoolean("hasIndicator", false),
            indicatorPct = obj.optDouble("indicatorPct", 0.0),
        )
    }
}

data class DeckSnapshot(
    val activeProfileId: String,
    val activeProfileName: String,
    val followHost: Boolean,
    val profiles: List<DeckProfile>,
    val cols: Int,
    val rows: Int,
    val dials: Int,
    val pageIndex: Int,
    val pageCount: Int,
    val pageName: String,
    val folderPath: List<String>,
    val inFolder: Boolean,
    val slots: Map<String, DeckSlot>,
    val dialSlots: List<DeckDial>,
) {
    fun toCompactWatchJson(): String {
        val root = JSONObject()
        root.put("activeProfileId", activeProfileId)
        root.put("activeProfileName", activeProfileName)
        root.put("followHost", followHost)

        val profArr = JSONArray()
        for (p in profiles) {
            profArr.put(
                JSONObject().apply {
                    put("id", p.id)
                    put("name", p.name)
                },
            )
        }
        root.put("profiles", profArr)

        root.put(
            "grid",
            JSONObject().apply {
                put("cols", cols)
                put("rows", rows)
            },
        )
        root.put("dials", dials)
        root.put("pageIndex", pageIndex)
        root.put("pageCount", pageCount)
        root.put("pageName", pageName)

        if (folderPath.isNotEmpty()) {
            val fArr = JSONArray()
            for (f in folderPath) fArr.put(f)
            root.put("folderPath", fArr)
        }
        root.put("inFolder", inFolder)

        val slotsObj = JSONObject()
        for ((k, slot) in slots) {
            if (slot.assigned || slot.autoBack) {
                slotsObj.put(k, slot.toJson())
            }
        }
        root.put("slots", slotsObj)

        val dialsArr = JSONArray()
        for (d in dialSlots) {
            dialsArr.put(d.toJson())
        }
        root.put("dialSlots", dialsArr)
        return root.toString()
    }

    companion object {
        fun fromJson(obj: JSONObject): DeckSnapshot {
            val profiles = buildList {
                val arr = obj.optJSONArray("profiles")
                if (arr != null) {
                    for (i in 0 until arr.length()) {
                        val p = arr.optJSONObject(i) ?: continue
                        val id = p.optString("id", "")
                        if (id.isNotEmpty()) {
                            add(DeckProfile(id = id, name = p.optString("name", id)))
                        }
                    }
                }
            }

            val gridObj = obj.optJSONObject("grid")
            val cols = (gridObj?.optInt("cols", 5) ?: 5).coerceAtLeast(1)
            val rows = (gridObj?.optInt("rows", 3) ?: 3).coerceAtLeast(1)

            val folderPath = buildList {
                val arr = obj.optJSONArray("folderPath")
                if (arr != null) {
                    for (i in 0 until arr.length()) {
                        val segment = arr.optString(i, "")
                        if (segment.isNotEmpty()) add(segment)
                    }
                }
            }

            val slots = LinkedHashMap<String, DeckSlot>()
            val slotsObj = obj.optJSONObject("slots")
            if (slotsObj != null) {
                val keys = slotsObj.keys()
                while (keys.hasNext()) {
                    val key = keys.next()
                    val sObj = slotsObj.optJSONObject(key) ?: continue
                    slots[key] = DeckSlot.fromJson(sObj)
                }
            }

            val dialSlots = buildList {
                val arr = obj.optJSONArray("dialSlots")
                if (arr != null) {
                    for (i in 0 until arr.length()) {
                        val dObj = arr.optJSONObject(i) ?: continue
                        add(DeckDial.fromJson(dObj))
                    }
                }
            }

            return DeckSnapshot(
                activeProfileId = obj.optString("activeProfileId", ""),
                activeProfileName = obj.optString("activeProfileName", "Default"),
                followHost = obj.optBoolean("followHost", false),
                profiles = profiles,
                cols = cols,
                rows = rows,
                dials = obj.optInt("dials", 0).coerceAtLeast(0),
                pageIndex = obj.optInt("pageIndex", 0).coerceAtLeast(0),
                pageCount = obj.optInt("pageCount", 1).coerceAtLeast(1),
                pageName = obj.optString("pageName", "Page 1"),
                folderPath = folderPath,
                inFolder = obj.optBoolean("inFolder", false),
                slots = slots,
                dialSlots = dialSlots,
            )
        }

        fun fromJsonString(raw: String): DeckSnapshot? = try {
            fromJson(JSONObject(raw))
        } catch (_: Exception) {
            null
        }
    }
}

data class WatchStatusMessage(
    val state: String,
    val detail: String,
) {
    fun toJson(): String = JSONObject().apply {
        put("state", state)
        put("detail", detail)
    }.toString()

    companion object {
        const val STATE_CONNECTING = "connecting"
        const val STATE_LIVE = "live"
        const val STATE_OFFLINE = "offline"
        const val STATE_UNPAIRED = "unpaired"
        const val STATE_ERROR = "error"

        fun fromJson(raw: String): WatchStatusMessage? = try {
            val obj = JSONObject(raw)
            val state = obj.optString("state", "")
            if (state.isEmpty()) null else WatchStatusMessage(state, obj.optString("detail", ""))
        } catch (_: Exception) {
            null
        }
    }
}

object DeckCommand {
    fun ping(): String = JSONObject().put("type", "ping").toString()

    fun keyDown(row: Int, col: Int): String = JSONObject()
        .put("type", "keyDown")
        .put("row", row)
        .put("col", col)
        .toString()

    fun keyUp(row: Int, col: Int): String = JSONObject()
        .put("type", "keyUp")
        .put("row", row)
        .put("col", col)
        .toString()

    fun dialRotate(dial: Int, ticks: Int): String = JSONObject()
        .put("type", "dialRotate")
        .put("dial", dial)
        .put("ticks", ticks.coerceIn(-20, 20))
        .toString()

    fun dialPress(dial: Int): String = JSONObject()
        .put("type", "dialPress")
        .put("dial", dial)
        .toString()

    fun prevPage(): String = JSONObject().put("type", "prevPage").toString()

    fun nextPage(): String = JSONObject().put("type", "nextPage").toString()

    fun closeFolder(): String = JSONObject().put("type", "closeFolder").toString()

    fun switchProfile(profileId: String): String = JSONObject()
        .put("type", "switchProfile")
        .put("profileId", profileId)
        .toString()

    fun configureSession(ephemeral: Boolean, profileId: String = ""): String = JSONObject()
        .put("type", "configureSession")
        .put("ephemeral", ephemeral)
        .apply {
            if (profileId.isNotBlank()) put("profileId", profileId)
        }
        .toString()
}

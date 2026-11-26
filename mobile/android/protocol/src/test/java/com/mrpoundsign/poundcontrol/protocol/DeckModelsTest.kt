package com.mrpoundsign.poundcontrol.protocol

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DeckModelsTest {
    @Test
    fun compactWatchJsonStripsImagesAndPreservesButtonsAndDials() {
        val raw = """
            {
              "activeProfileId": "prof-watch",
              "activeProfileName": "Watch",
              "followHost": false,
              "profiles": [
                {"id": "prof-default", "name": "Default"},
                {"id": "prof-watch", "name": "Watch"}
              ],
              "grid": {"cols": 2, "rows": 2},
              "dials": 1,
              "pageIndex": 1,
              "pageCount": 2,
              "pageName": "OBS",
              "folderPath": ["Audio"],
              "inFolder": true,
              "slots": {
                "0,0": {
                  "assigned": false,
                  "autoBack": true,
                  "title": "← Back",
                  "subtitle": "Up",
                  "color": "#94A3B8"
                },
                "0,1": {
                  "assigned": true,
                  "title": "Mute Mic",
                  "subtitle": "LIVE",
                  "label": "Playing",
                  "sublabel": "Track A",
                  "color": "#EF4444",
                  "bgColor": "#1E293B",
                  "imageUrl": "data:image/png;base64,AAAA",
                  "bgImageUrl": "/api/asset/123?sid=x&mac=y",
                  "state": 1,
                  "flash": "ok"
                },
                "1,1": {
                  "assigned": false
                }
              },
              "dialSlots": [
                {
                  "index": 0,
                  "assigned": true,
                  "title": "Mic Vol",
                  "value": "72%",
                  "color": "#38BDF8",
                  "hasIndicator": true,
                  "indicatorPct": 72.0
                }
              ]
            }
        """.trimIndent()

        val snap = DeckSnapshot.fromJsonString(raw)
        assertNotNull(snap)
        snap!!

        val compact = snap.toCompactWatchJson()
        assertFalse(compact.contains("imageUrl"))
        assertFalse(compact.contains("bgImageUrl"))
        assertFalse(compact.contains("1,1"))

        val reparsed = DeckSnapshot.fromJsonString(compact)
        assertNotNull(reparsed)
        reparsed!!
        assertEquals("prof-watch", reparsed.activeProfileId)
        assertEquals(2, reparsed.cols)
        assertEquals(2, reparsed.rows)
        assertTrue(reparsed.inFolder)
        assertEquals(listOf("Audio"), reparsed.folderPath)
        assertEquals("Mute Mic", reparsed.slots["0,1"]?.title)
        assertEquals("LIVE", reparsed.slots["0,1"]?.subtitle)
        assertEquals("Playing", reparsed.slots["0,1"]?.label)
        assertEquals("Track A", reparsed.slots["0,1"]?.sublabel)
        assertEquals(1, reparsed.slots["0,1"]?.state)
        assertEquals("← Back", reparsed.slots["0,0"]?.title)
        assertEquals(1, reparsed.dialSlots.size)
        assertEquals(72.0, reparsed.dialSlots[0].indicatorPct, 0.01)
    }

    @Test
    fun authRecordAndCommandsRoundTrip() {
        val rec = AuthRecord.fromJson(
            """{"hostId":"h1","hostName":"Studio","hostPublicKey":"pk","deviceId":"d1","deviceName":"Pixel","clientSeed":"seed","clientPublicKey":"cpk"}""",
        )
        assertNotNull(rec)
        assertEquals("d1", rec!!.deviceId)

        val cfgCmd = JSONObject(DeckCommand.configureSession(ephemeral = true, profileId = "Watch"))
        assertEquals("configureSession", cfgCmd.getString("type"))
        assertTrue(cfgCmd.getBoolean("ephemeral"))
        assertEquals("Watch", cfgCmd.getString("profileId"))

        val rotCmd = JSONObject(DeckCommand.dialRotate(0, 99))
        assertEquals(20, rotCmd.getInt("ticks"))
    }
}

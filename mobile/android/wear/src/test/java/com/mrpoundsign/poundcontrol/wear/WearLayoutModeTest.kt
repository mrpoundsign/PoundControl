package com.mrpoundsign.poundcontrol.wear

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class WearLayoutModeTest {

    @Test
    fun layoutModeFromStorageKeyFallsBackToAuto() {
        assertEquals(LayoutMode.AUTO, LayoutMode.fromStorageKey("auto"))
        assertEquals(LayoutMode.GRID, LayoutMode.fromStorageKey("grid"))
        assertEquals(LayoutMode.LIST, LayoutMode.fromStorageKey("list"))
        assertEquals(LayoutMode.AUTO, LayoutMode.fromStorageKey(null))
        assertEquals(LayoutMode.AUTO, LayoutMode.fromStorageKey("unknown"))
        assertEquals(LayoutMode.AUTO, LayoutMode.fromStorageKey(""))
    }

    @Test
    fun shouldUseGridLayoutHandlesAutoGridAndListModes() {
        assertTrue(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 2, rows = 2))
        assertTrue(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 2, rows = 3))
        assertTrue(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 3, rows = 2))
        assertFalse(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 5, rows = 3))
        assertFalse(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 4, rows = 2))
        assertFalse(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 0, rows = 2))
        assertFalse(WearDeckHelper.shouldUseGridLayout(LayoutMode.AUTO, cols = 2, rows = 0))

        assertTrue(WearDeckHelper.shouldUseGridLayout(LayoutMode.GRID, cols = 5, rows = 3))
        assertTrue(WearDeckHelper.shouldUseGridLayout(LayoutMode.GRID, cols = 2, rows = 2))

        assertFalse(WearDeckHelper.shouldUseGridLayout(LayoutMode.LIST, cols = 2, rows = 2))
        assertFalse(WearDeckHelper.shouldUseGridLayout(LayoutMode.LIST, cols = 5, rows = 3))
    }

    @Test
    fun accumulateCrownTicksAccumulatesClampsAndGuardsInvalidInputs() {
        val step1 = WearDeckHelper.accumulateCrownTicks(accum = 0f, delta = 0.40f, threshold = 0.75f)
        assertEquals(0, step1.ticks)
        assertEquals(0.40f, step1.remainingAccum, 0.0001f)

        val step2 = WearDeckHelper.accumulateCrownTicks(
            accum = step1.remainingAccum,
            delta = 0.50f,
            threshold = 0.75f,
        )
        assertEquals(1, step2.ticks)
        assertEquals(0.15f, step2.remainingAccum, 0.0001f)

        val neg = WearDeckHelper.accumulateCrownTicks(accum = -0.25f, delta = -1.30f, threshold = 0.75f)
        assertEquals(-2, neg.ticks)
        assertEquals(-0.05f, neg.remainingAccum, 0.0001f)

        val clampedPos = WearDeckHelper.accumulateCrownTicks(accum = 0f, delta = 50f, threshold = 0.75f, maxStep = 20)
        assertEquals(20, clampedPos.ticks)
        assertEquals(0f, clampedPos.remainingAccum, 0.0001f)

        val clampedNeg = WearDeckHelper.accumulateCrownTicks(accum = 0f, delta = -50f, threshold = 0.75f, maxStep = 20)
        assertEquals(-20, clampedNeg.ticks)
        assertEquals(0f, clampedNeg.remainingAccum, 0.0001f)

        assertEquals(CrownTickResult(0, 0f), WearDeckHelper.accumulateCrownTicks(0f, Float.NaN))
        assertEquals(CrownTickResult(0, 0f), WearDeckHelper.accumulateCrownTicks(0f, 1f, threshold = 0f))
        assertEquals(CrownTickResult(0, 0f), WearDeckHelper.accumulateCrownTicks(0f, 1f, maxStep = 0))

        // Pixel Watch scaled rotary scroll (100px / 2.0 density = 50dp per scroll unit, threshold 14dp).
        val deltaDp = WearDeckHelper.scaleRotaryDeltaDp(
            rawAxisScroll = -0.35f,
            verticalScrollFactorPx = 100f,
            density = 2.0f,
        )
        assertEquals(17.5f, deltaDp, 0.0001f)
        val scaledStep = WearDeckHelper.accumulateCrownTicks(
            accum = 0f,
            delta = deltaDp,
            threshold = WearDeckHelper.DEFAULT_CROWN_STEP_DP,
        )
        assertEquals(1, scaledStep.ticks)
        assertEquals(3.5f, scaledStep.remainingAccum, 0.0001f)

        assertEquals(0f, WearDeckHelper.scaleRotaryDeltaDp(Float.NaN, 100f, 2.0f), 0.0001f)
        assertEquals(-32f, WearDeckHelper.scaleRotaryDeltaDp(1f, 0f, 0f), 0.0001f)
    }

    @Test
    fun parseHexArgbAndContrastForegroundPickReadableColors() {
        assertEquals(0xFF38BDF8.toInt(), WearDeckHelper.parseHexArgbOrNull("#38BDF8"))
        assertEquals(0xFFFFAA00.toInt(), WearDeckHelper.parseHexArgbOrNull("#FA0"))
        assertEquals(0x801E293B.toInt(), WearDeckHelper.parseHexArgbOrNull("#801E293B"))
        assertNull(WearDeckHelper.parseHexArgbOrNull(null))
        assertNull(WearDeckHelper.parseHexArgbOrNull(""))
        assertNull(WearDeckHelper.parseHexArgbOrNull("#12345"))
        assertNull(WearDeckHelper.parseHexArgbOrNull("#GGGGGG"))

        assertEquals(
            WearDeckHelper.OBSIDIAN_SLATE_ARGB,
            WearDeckHelper.contrastForegroundArgb(WearDeckHelper.SKY_BLUE_ARGB),
        )
        assertEquals(
            WearDeckHelper.OBSIDIAN_SLATE_ARGB,
            WearDeckHelper.contrastForegroundArgb(WearDeckHelper.CHROME_WHITE_ARGB),
        )
        assertEquals(
            WearDeckHelper.CHROME_WHITE_ARGB,
            WearDeckHelper.contrastForegroundArgb(WearDeckHelper.OBSIDIAN_SLATE_ARGB),
        )
        assertEquals(
            WearDeckHelper.CHROME_WHITE_ARGB,
            WearDeckHelper.contrastForegroundArgb(0xFF14161D.toInt()),
        )
    }

    @Test
    fun chromaAndBlendHelpersMatchGlowThresholds() {
        assertTrue(WearDeckHelper.isChromaticArgb(WearDeckHelper.SKY_BLUE_ARGB))
        assertTrue(WearDeckHelper.isChromaticArgb(WearDeckHelper.STATUS_ALERT_ARGB))
        assertFalse(WearDeckHelper.isChromaticArgb(WearDeckHelper.OBSIDIAN_SLATE_ARGB))
        assertFalse(WearDeckHelper.isChromaticArgb(WearDeckHelper.CHROME_WHITE_ARGB))
        assertFalse(WearDeckHelper.isChromaticArgb(0xFF1E293B.toInt()))
        assertFalse(WearDeckHelper.isChromaticArgb(0xFF323261.toInt())) // diff = 47
        assertTrue(WearDeckHelper.isChromaticArgb(0xFF323262.toInt())) // diff = 48

        assertEquals(0x38ABCDEF, WearDeckHelper.withAlpha(0xFFABCDEF.toInt(), 0x38))
        assertEquals(0x00ABCDEF, WearDeckHelper.withAlpha(0xFFABCDEF.toInt(), -5))
        assertEquals(0xFFABCDEF.toInt(), WearDeckHelper.withAlpha(0x11ABCDEF, 999))

        assertEquals(
            WearDeckHelper.SURFACE_1_ARGB,
            WearDeckHelper.blendArgb(WearDeckHelper.SURFACE_1_ARGB, WearDeckHelper.SKY_BLUE_ARGB, 0f),
        )
        assertEquals(
            WearDeckHelper.SKY_BLUE_ARGB,
            WearDeckHelper.blendArgb(WearDeckHelper.SURFACE_1_ARGB, WearDeckHelper.SKY_BLUE_ARGB, 1f),
        )
        assertEquals(
            0xFF808080.toInt(),
            WearDeckHelper.blendArgb(0xFF000000.toInt(), 0xFFFFFFFF.toInt(), 0.5f),
        )
        assertEquals(
            0xFF1A2F3A.toInt(),
            WearDeckHelper.blendArgb(WearDeckHelper.SURFACE_1_ARGB, WearDeckHelper.SKY_BLUE_ARGB, 0.16f),
        )
        assertEquals(
            WearDeckHelper.CHROME_WHITE_ARGB,
            WearDeckHelper.blendArgb(WearDeckHelper.OBSIDIAN_SLATE_ARGB, WearDeckHelper.CHROME_WHITE_ARGB, 2.5f),
        )
    }
}

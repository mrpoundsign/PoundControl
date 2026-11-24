package com.mrpoundsign.poundcontrol.wear

import kotlin.math.pow
import kotlin.math.roundToInt

/**
 * Display layout preference for the Wear OS Keys screen.
 */
enum class LayoutMode(val storageKey: String, val label: String) {
    AUTO("auto", "Auto"),
    GRID("grid", "Grid"),
    LIST("list", "List"),
    ;

    companion object {
        fun fromStorageKey(key: String?): LayoutMode {
            return entries.firstOrNull { it.storageKey == key } ?: AUTO
        }
    }
}

/**
 * Outcome of accumulating a rotary crown scroll delta toward discrete dial ticks.
 */
data class CrownTickResult(
    val ticks: Int,
    val remainingAccum: Float,
)

/**
 * Pure-Kotlin helpers for Wear layout selection, rotary encoder accumulation, and color contrast.
 */
object WearDeckHelper {
    const val OBSIDIAN_SLATE_ARGB: Int = 0xFF0A0A0C.toInt()
    const val SURFACE_1_ARGB: Int = 0xFF141416.toInt()
    const val BORDER_SLATE_ARGB: Int = 0xFF475569.toInt()
    const val CHROME_WHITE_ARGB: Int = 0xFFFFFFFF.toInt()
    const val SKY_BLUE_ARGB: Int = 0xFF38BDF8.toInt()
    const val STATUS_OK_ARGB: Int = 0xFF22C55E.toInt()
    const val STATUS_ALERT_ARGB: Int = 0xFFEF4444.toInt()

    /**
     * Returns true when the Keys view should render as a compact grid instead of a scrollable list.
     * In [LayoutMode.AUTO], grids with at most 6 slots use the grid layout; larger pages switch to list.
     */
    fun shouldUseGridLayout(mode: LayoutMode, cols: Int, rows: Int): Boolean {
        return when (mode) {
            LayoutMode.GRID -> true
            LayoutMode.LIST -> false
            LayoutMode.AUTO -> cols > 0 && rows > 0 && (cols * rows) <= 6
        }
    }

    /**
     * Accumulates a rotary encoder [delta] (typically `-axisScroll` so clockwise yields positive ticks)
     * and emits integer ticks once [threshold] is reached, clamping per-event steps to `[-maxStep, maxStep]`.
     */
    fun accumulateCrownTicks(
        accum: Float,
        delta: Float,
        threshold: Float = 0.75f,
        maxStep: Int = 20,
    ): CrownTickResult {
        if (!delta.isFinite() || threshold <= 0f || maxStep <= 0) {
            return CrownTickResult(ticks = 0, remainingAccum = 0f)
        }
        val safeAccum = if (accum.isFinite()) accum else 0f
        val combined = safeAccum + delta
        val rawSteps = (combined / threshold).toInt()
        val clamped = rawSteps.coerceIn(-maxStep, maxStep)
        val remainder = if (rawSteps == clamped) {
            combined - clamped * threshold
        } else {
            0f
        }
        return CrownTickResult(ticks = clamped, remainingAccum = remainder)
    }

    /**
     * Parses `#RGB`, `#RRGGBB`, or `#AARRGGBB` hex strings into a 32-bit ARGB color integer.
     */
    fun parseHexArgbOrNull(hex: String?): Int? {
        val raw = hex?.trim()?.removePrefix("#") ?: return null
        if (raw.isEmpty()) return null
        return when (raw.length) {
            3 -> {
                val r = raw[0].digitToIntOrNull(16) ?: return null
                val g = raw[1].digitToIntOrNull(16) ?: return null
                val b = raw[2].digitToIntOrNull(16) ?: return null
                val rr = (r shl 4) or r
                val gg = (g shl 4) or g
                val bb = (b shl 4) or b
                (0xFF shl 24) or (rr shl 16) or (gg shl 8) or bb
            }
            6 -> {
                val rgb = raw.toIntOrNull(16) ?: return null
                (0xFF shl 24) or rgb
            }
            8 -> {
                raw.toLongOrNull(16)?.toInt()
            }
            else -> null
        }
    }

    /**
     * Computes WCAG 2.1 relative luminance for the RGB channels of an ARGB color in `[0.0, 1.0]`.
     */
    fun relativeLuminance(argb: Int): Double {
        fun linearize(channel: Int): Double {
            val srgb = (channel and 0xFF) / 255.0
            return if (srgb <= 0.04045) {
                srgb / 12.92
            } else {
                ((srgb + 0.055) / 1.055).pow(2.4)
            }
        }
        val r = linearize(argb ushr 16)
        val g = linearize(argb ushr 8)
        val b = linearize(argb)
        return 0.2126 * r + 0.7152 * g + 0.0722 * b
    }

    /**
     * Returns [darkArgb] or [lightArgb], whichever yields higher WCAG contrast against [bgArgb].
     */
    fun contrastForegroundArgb(
        bgArgb: Int,
        darkArgb: Int = OBSIDIAN_SLATE_ARGB,
        lightArgb: Int = CHROME_WHITE_ARGB,
    ): Int {
        val bgLum = relativeLuminance(bgArgb)
        val darkLum = relativeLuminance(darkArgb)
        val lightLum = relativeLuminance(lightArgb)
        val contrastDark = (maxOf(bgLum, darkLum) + 0.05) / (minOf(bgLum, darkLum) + 0.05)
        val contrastLight = (maxOf(bgLum, lightLum) + 0.05) / (minOf(bgLum, lightLum) + 0.05)
        return if (contrastDark >= contrastLight) darkArgb else lightArgb
    }

    /**
     * Returns true when the color has enough chroma (`max(r,g,b) - min(r,g,b) >= 48`) to emit
     * an ambient border and accent halo, matching WebDeck's `isChromaticHex`.
     */
    fun isChromaticArgb(argb: Int): Boolean {
        val r = (argb ushr 16) and 0xFF
        val g = (argb ushr 8) and 0xFF
        val b = argb and 0xFF
        return maxOf(r, g, b) - minOf(r, g, b) >= 48
    }

    /**
     * Replaces the alpha channel of [argb] with [alpha] clamped to `[0, 255]`.
     */
    fun withAlpha(argb: Int, alpha: Int): Int {
        val clamped = alpha.coerceIn(0, 255)
        return (clamped shl 24) or (argb and 0x00FFFFFF)
    }

    /**
     * Linearly blends [baseArgb] toward [overlayArgb] by [ratio] clamped to `[0f, 1f]`.
     */
    fun blendArgb(baseArgb: Int, overlayArgb: Int, ratio: Float): Int {
        val t = if (ratio.isFinite()) ratio.coerceIn(0f, 1f) else 0f
        val inv = 1f - t
        val a = (((baseArgb ushr 24) and 0xFF) * inv + ((overlayArgb ushr 24) and 0xFF) * t)
            .roundToInt().coerceIn(0, 255)
        val r = (((baseArgb ushr 16) and 0xFF) * inv + ((overlayArgb ushr 16) and 0xFF) * t)
            .roundToInt().coerceIn(0, 255)
        val g = (((baseArgb ushr 8) and 0xFF) * inv + ((overlayArgb ushr 8) and 0xFF) * t)
            .roundToInt().coerceIn(0, 255)
        val b = ((baseArgb and 0xFF) * inv + (overlayArgb and 0xFF) * t)
            .roundToInt().coerceIn(0, 255)
        return (a shl 24) or (r shl 16) or (g shl 8) or b
    }
}

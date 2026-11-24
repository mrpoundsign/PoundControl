package com.mrpoundsign.poundcontrol.wear

import android.content.Context
import android.graphics.Canvas
import android.graphics.Matrix
import android.graphics.Paint
import android.graphics.Path
import android.graphics.RadialGradient
import android.graphics.RectF
import android.graphics.Shader
import android.util.AttributeSet
import android.widget.LinearLayout
import kotlin.math.min
import kotlin.math.roundToInt

/**
 * Hardware-accelerated deck tile container rendering PoundControl's Obsidian Slate card surface
 * with stepped neon outer halo, inner radial illumination, glowing accent bar, flash highlight,
 * and active breathing shimmer.
 */
class GlowTileView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = 0,
) : LinearLayout(context, attrs, defStyleAttr) {

    enum class AccentBarPosition {
        TOP,
        LEFT,
    }

    private val density = resources.displayMetrics.density
    private val haloInsetPx = 4f * density

    private val cardRect = RectF()
    private val barRect = RectF()
    private val barHaloRect = RectF()
    private val clipPath = Path()
    private val bloomMatrix = Matrix()

    private val haloPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
    }
    private val fillPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }
    private val innerGlowPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
    }
    private val bloomPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }
    private val pressPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }
    private val borderPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
    }
    private val barHaloPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }
    private val barPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }

    private var assignedOrAutoBack: Boolean = false
    private var accentArgb: Int = WearDeckHelper.SKY_BLUE_ARGB
    private var customBgArgb: Int? = null
    private var activeState: Boolean = false
    private var flashState: String = ""
    private var barPosition: AccentBarPosition = AccentBarPosition.TOP
    private var cornerRadiusPx: Float = 12f * density
    private var shimmerLevel: Float = 0.5f

    private var cachedBloomColor: Int = 0
    private var cachedBloomShader: RadialGradient? = null

    init {
        setWillNotDraw(false)
        clipChildren = false
        clipToPadding = false
    }

    fun configure(
        assignedOrAutoBack: Boolean,
        accentArgb: Int = WearDeckHelper.SKY_BLUE_ARGB,
        customBgArgb: Int? = null,
        active: Boolean = false,
        flash: String = "",
        barPosition: AccentBarPosition = AccentBarPosition.TOP,
        cornerRadiusDp: Float = 12f,
    ) {
        this.assignedOrAutoBack = assignedOrAutoBack
        this.accentArgb = accentArgb
        this.customBgArgb = customBgArgb
        this.activeState = active
        this.flashState = flash
        this.barPosition = barPosition
        this.cornerRadiusPx = cornerRadiusDp * density
        rebuildClipPath()
        invalidate()
    }

    fun setShimmerLevel(level: Float) {
        val clamped = if (level.isFinite()) level.coerceIn(0f, 1f) else 0.5f
        if (clamped == shimmerLevel) return
        shimmerLevel = clamped
        if (activeState && assignedOrAutoBack) {
            invalidate()
        }
    }

    override fun drawableStateChanged() {
        super.drawableStateChanged()
        val targetScale = if (isPressed && assignedOrAutoBack) 0.96f else 1f
        if (scaleX != targetScale) {
            scaleX = targetScale
            scaleY = targetScale
        }
        invalidate()
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        rebuildClipPath()
    }

    private fun rebuildClipPath() {
        val w = width.toFloat()
        val h = height.toFloat()
        if (w <= haloInsetPx * 2f || h <= haloInsetPx * 2f) {
            clipPath.reset()
            return
        }
        cardRect.set(haloInsetPx, haloInsetPx, w - haloInsetPx, h - haloInsetPx)
        clipPath.reset()
        clipPath.addRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, Path.Direction.CW)
    }

    override fun onDraw(canvas: Canvas) {
        if (width <= haloInsetPx * 2f || height <= haloInsetPx * 2f) {
            super.onDraw(canvas)
            return
        }
        if (clipPath.isEmpty) {
            rebuildClipPath()
        }

        if (!assignedOrAutoBack) {
            fillPaint.color = 0xFF0E1016.toInt()
            canvas.drawRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, fillPaint)
            borderPaint.color = 0xFF1A1E28.toInt()
            borderPaint.strokeWidth = 1f * density
            canvas.drawRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, borderPaint)
            super.onDraw(canvas)
            return
        }

        val ambientGlow = WearDeckHelper.isChromaticArgb(accentArgb)
        val hasFlash = flashState == "ok" || flashState == "alert"
        val glowColor = when (flashState) {
            "ok" -> WearDeckHelper.STATUS_OK_ARGB
            "alert" -> WearDeckHelper.STATUS_ALERT_ARGB
            else -> accentArgb
        }
        val shimmerAlpha = 0.42f + 0.58f * shimmerLevel
        val barBreathe = 0.84f + 0.16f * shimmerLevel

        // Stepped outer neon halo centered along card border; fill covers inner half cleanly.
        when {
            hasFlash -> {
                drawHaloStroke(canvas, glowColor, alpha = 24, extraWidthDp = 8.0f)
                drawHaloStroke(canvas, glowColor, alpha = 62, extraWidthDp = 5.0f)
                drawHaloStroke(canvas, glowColor, alpha = 120, extraWidthDp = 2.4f)
            }
            activeState -> {
                drawHaloStroke(canvas, glowColor, alpha = (28f * shimmerAlpha).roundToInt(), extraWidthDp = 8.0f)
                drawHaloStroke(canvas, glowColor, alpha = (70f * shimmerAlpha).roundToInt(), extraWidthDp = 5.0f)
                drawHaloStroke(canvas, glowColor, alpha = (130f * shimmerAlpha).roundToInt(), extraWidthDp = 2.4f)
            }
            ambientGlow -> {
                drawHaloStroke(canvas, accentArgb, alpha = 12, extraWidthDp = 7.4f)
                drawHaloStroke(canvas, accentArgb, alpha = 28, extraWidthDp = 4.6f)
                drawHaloStroke(canvas, accentArgb, alpha = 58, extraWidthDp = 2.2f)
            }
        }

        // Card fill: custom background color, or 16% accent blend when active, else surface-1.
        val fillArgb = customBgArgb ?: if (activeState) {
            WearDeckHelper.blendArgb(WearDeckHelper.SURFACE_1_ARGB, accentArgb, 0.16f)
        } else {
            WearDeckHelper.SURFACE_1_ARGB
        }
        fillPaint.color = fillArgb
        canvas.drawRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, fillPaint)

        // Inner illumination, top radial bloom, and accent bar clipped smoothly inside the card.
        val saveCount = canvas.save()
        canvas.clipPath(clipPath)

        if (ambientGlow || activeState || hasFlash) {
            val innerAlpha = when {
                activeState -> (82f * shimmerAlpha).roundToInt()
                hasFlash -> 74
                else -> 38
            }
            innerGlowPaint.color = WearDeckHelper.withAlpha(glowColor, innerAlpha)
            innerGlowPaint.strokeWidth = 5.5f * density
            canvas.drawRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, innerGlowPaint)
        }

        if (activeState) {
            val bloomArgb = WearDeckHelper.withAlpha(accentArgb, (98f * shimmerAlpha).roundToInt())
            val shader = obtainBloomShader(bloomArgb)
            val cw = cardRect.width().coerceAtLeast(1f)
            val ch = cardRect.height().coerceAtLeast(1f)
            bloomMatrix.reset()
            bloomMatrix.setScale(cw * 0.44f, ch * 0.58f)
            bloomMatrix.postTranslate(cardRect.left + cw * 0.5f, cardRect.top + ch * 0.06f)
            shader.setLocalMatrix(bloomMatrix)
            bloomPaint.shader = shader
            canvas.drawRect(cardRect, bloomPaint)
        }

        if (isPressed) {
            pressPaint.color = WearDeckHelper.withAlpha(glowColor, 54)
            canvas.drawRect(cardRect, pressPaint)
        }

        drawAccentBar(canvas, ambientGlow, shimmerAlpha, barBreathe)

        canvas.restoreToCount(saveCount)

        // Crisp card border.
        val borderArgb = when {
            flashState == "ok" -> WearDeckHelper.STATUS_OK_ARGB
            flashState == "alert" -> WearDeckHelper.STATUS_ALERT_ARGB
            activeState -> WearDeckHelper.blendArgb(accentArgb, WearDeckHelper.CHROME_WHITE_ARGB, 0.16f)
            else -> accentArgb
        }
        borderPaint.color = borderArgb
        borderPaint.strokeWidth = when {
            hasFlash -> 2.0f * density
            activeState -> 1.8f * density
            else -> 1.2f * density
        }
        canvas.drawRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, borderPaint)

        super.onDraw(canvas)
    }

    private fun drawHaloStroke(canvas: Canvas, color: Int, alpha: Int, extraWidthDp: Float) {
        if (alpha <= 0) return
        haloPaint.color = WearDeckHelper.withAlpha(color, alpha)
        haloPaint.strokeWidth = extraWidthDp * density
        canvas.drawRoundRect(cardRect, cornerRadiusPx, cornerRadiusPx, haloPaint)
    }

    private fun obtainBloomShader(colorArgb: Int): RadialGradient {
        val existing = cachedBloomShader
        if (existing != null && cachedBloomColor == colorArgb) {
            return existing
        }
        val created = RadialGradient(
            0f,
            0f,
            1f,
            colorArgb,
            0,
            Shader.TileMode.CLAMP,
        )
        cachedBloomColor = colorArgb
        cachedBloomShader = created
        return created
    }

    private fun drawAccentBar(
        canvas: Canvas,
        ambientGlow: Boolean,
        shimmerAlpha: Float,
        barBreathe: Float,
    ) {
        when (barPosition) {
            AccentBarPosition.TOP -> {
                val cw = cardRect.width()
                val left = cardRect.left + cw * 0.20f
                val right = cardRect.right - cw * 0.20f
                val top = cardRect.top + 4.5f * density
                val bottom = top + 3.0f * density
                barRect.set(left, top, right, bottom)
            }
            AccentBarPosition.LEFT -> {
                val left = cardRect.left + 6.5f * density
                val right = left + 3.5f * density
                val barH = min(22f * density, cardRect.height() * 0.54f)
                val cy = cardRect.centerY()
                barRect.set(left, cy - barH * 0.5f, right, cy + barH * 0.5f)
            }
        }

        val barColor = if (activeState) {
            WearDeckHelper.blendArgb(accentArgb, WearDeckHelper.CHROME_WHITE_ARGB, 0.26f)
        } else {
            accentArgb
        }
        val pillRadius = 999f

        if (activeState || ambientGlow) {
            val outerAlpha = if (activeState) (72f * barBreathe).roundToInt() else 34
            val innerAlpha = if (activeState) (132f * barBreathe * shimmerAlpha.coerceAtLeast(0.65f)).roundToInt() else 66

            barHaloRect.set(
                barRect.left - 3.5f * density,
                barRect.top - 2.4f * density,
                barRect.right + 3.5f * density,
                barRect.bottom + 2.4f * density,
            )
            barHaloPaint.color = WearDeckHelper.withAlpha(accentArgb, outerAlpha)
            canvas.drawRoundRect(barHaloRect, pillRadius, pillRadius, barHaloPaint)

            barHaloRect.set(
                barRect.left - 1.6f * density,
                barRect.top - 1.2f * density,
                barRect.right + 1.6f * density,
                barRect.bottom + 1.2f * density,
            )
            barHaloPaint.color = WearDeckHelper.withAlpha(accentArgb, innerAlpha)
            canvas.drawRoundRect(barHaloRect, pillRadius, pillRadius, barHaloPaint)
        }

        barPaint.color = if (activeState) {
            WearDeckHelper.withAlpha(barColor, (255f * barBreathe).roundToInt())
        } else {
            barColor
        }
        canvas.drawRoundRect(barRect, pillRadius, pillRadius, barPaint)
    }
}

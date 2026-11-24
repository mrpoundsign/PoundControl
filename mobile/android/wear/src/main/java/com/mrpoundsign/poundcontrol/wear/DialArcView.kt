package com.mrpoundsign.poundcontrol.wear

import android.content.Context
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.RadialGradient
import android.graphics.RectF
import android.graphics.Shader
import android.graphics.Typeface
import android.text.TextPaint
import android.text.TextUtils
import android.util.AttributeSet
import android.view.View
import com.mrpoundsign.poundcontrol.protocol.DeckDial
import kotlin.math.min

/**
 * Circular rotary dial gauge for Wear OS displaying a dark slate track, accent progress arc,
 * dial title, live value text, and tactile press highlight.
 */
class DialArcView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = 0,
) : View(context, attrs, defStyleAttr) {

    private val density = resources.displayMetrics.density
    private val baseStrokePx = 7f * density
    private val glowMarginPx = 8f * density
    private val arcBounds = RectF()

    private val fillPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }
    private val sheenPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
    }
    private val pressOverlayPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.FILL
        color = 0x3338BDF8
    }
    private val trackPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
        strokeWidth = baseStrokePx
        color = 0xFF1E293B.toInt()
    }
    private val arcGlowPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
    }
    private val arcPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
        strokeWidth = baseStrokePx
        color = WearDeckHelper.SKY_BLUE_ARGB
    }
    private val arcCorePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
        strokeWidth = 2.5f * density
    }
    private val titlePaint = TextPaint(Paint.ANTI_ALIAS_FLAG).apply {
        textAlign = Paint.Align.CENTER
        textSize = 12f * density
        typeface = Typeface.create(Typeface.DEFAULT, Typeface.BOLD)
        color = 0xFFCBD5E1.toInt()
    }
    private val valuePaint = TextPaint(Paint.ANTI_ALIAS_FLAG).apply {
        textAlign = Paint.Align.CENTER
        textSize = 16f * density
        typeface = Typeface.create(Typeface.DEFAULT, Typeface.BOLD)
        color = WearDeckHelper.CHROME_WHITE_ARGB
    }
    private val hintPaint = TextPaint(Paint.ANTI_ALIAS_FLAG).apply {
        textAlign = Paint.Align.CENTER
        textSize = 9.5f * density
        color = 0xFF64748B.toInt()
    }

    private var boundDial: DeckDial? = null
    private var dialPressed: Boolean = false
    private var cachedSheenAccent: Int = 0
    private var cachedSheenRadius: Float = -1f
    private var cachedSheenCx: Float = -1f
    private var cachedSheenCy: Float = -1f
    private var cachedSheenShader: RadialGradient? = null

    fun bind(dial: DeckDial?, isPressed: Boolean) {
        if (boundDial == dial && dialPressed == isPressed) return
        boundDial = dial
        dialPressed = isPressed
        invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val size = min(width - paddingLeft - paddingRight, height - paddingTop - paddingBottom).toFloat()
        if (size <= 0f) return

        val cx = width / 2f
        val cy = height / 2f
        val radius = (size - glowMarginPx * 2f - baseStrokePx) / 2f
        if (radius <= 0f) return

        val dial = boundDial
        val bgArgb = WearDeckHelper.parseHexArgbOrNull(dial?.bgColor) ?: 0xFF14161D.toInt()
        val accentArgb = WearDeckHelper.parseHexArgbOrNull(dial?.color) ?: WearDeckHelper.SKY_BLUE_ARGB
        val fgArgb = WearDeckHelper.contrastForegroundArgb(bgArgb)

        fillPaint.color = bgArgb
        canvas.drawCircle(cx, cy, radius, fillPaint)

        arcBounds.set(cx - radius, cy - radius, cx + radius, cy + radius)
        if (dial == null || !dial.assigned) {
            trackPaint.color = 0xFF1E293B.toInt()
            canvas.drawArc(arcBounds, 135f, 270f, false, trackPaint)
            titlePaint.color = 0xFF64748B.toInt()
            canvas.drawText("No Dial", cx, cy + (titlePaint.textSize * 0.35f), titlePaint)
            return
        }

        sheenPaint.shader = obtainSheenShader(cx, cy, radius, accentArgb)
        canvas.drawCircle(cx, cy, radius, sheenPaint)

        if (dialPressed) {
            pressOverlayPaint.color = WearDeckHelper.withAlpha(accentArgb, 56)
            canvas.drawCircle(cx, cy, radius, pressOverlayPaint)
        }

        trackPaint.color = 0xFF1E293B.toInt()
        val highlightArgb = WearDeckHelper.blendArgb(accentArgb, WearDeckHelper.CHROME_WHITE_ARGB, 0.35f)
        if (dial.hasIndicator) {
            canvas.drawArc(arcBounds, 135f, 270f, false, trackPaint)
            val pct = dial.indicatorPct.coerceIn(0.0, 100.0).toFloat()
            val sweep = (pct / 100f) * 270f
            if (sweep > 0.5f) {
                drawGlowingArc(canvas, startAngle = 135f, sweepAngle = sweep, accentArgb = accentArgb, highlightArgb = highlightArgb, mainAlpha = 255, highlightAlpha = 150, glowScale = 1f)
            }
        } else {
            canvas.drawArc(arcBounds, 0f, 360f, false, trackPaint)
            drawGlowingArc(canvas, startAngle = -135f, sweepAngle = 90f, accentArgb = accentArgb, highlightArgb = highlightArgb, mainAlpha = 200, highlightAlpha = 120, glowScale = 0.8f)
        }

        val maxTextWidth = (radius * 1.48f).coerceAtLeast(20f)
        val titleText = dial.title.ifBlank { "Dial ${dial.index + 1}" }
        val ellipsizedTitle = TextUtils.ellipsize(
            titleText,
            titlePaint,
            maxTextWidth,
            TextUtils.TruncateAt.END,
        ).toString()

        val valueText = dial.value.ifBlank {
            if (dial.hasIndicator) "${dial.indicatorPct.toInt()}%" else "Turn Crown"
        }
        val ellipsizedValue = TextUtils.ellipsize(
            valueText,
            valuePaint,
            maxTextWidth,
            TextUtils.TruncateAt.END,
        ).toString()

        titlePaint.color = if (fgArgb == WearDeckHelper.OBSIDIAN_SLATE_ARGB) {
            0xFF1E293B.toInt()
        } else {
            0xFFCBD5E1.toInt()
        }
        valuePaint.color = if (fgArgb == WearDeckHelper.OBSIDIAN_SLATE_ARGB) {
            WearDeckHelper.OBSIDIAN_SLATE_ARGB
        } else {
            WearDeckHelper.CHROME_WHITE_ARGB
        }
        hintPaint.color = if (fgArgb == WearDeckHelper.OBSIDIAN_SLATE_ARGB) {
            0xFF334155.toInt()
        } else {
            0xFF64748B.toInt()
        }

        canvas.drawText(ellipsizedTitle, cx, cy - 10f * density, titlePaint)
        canvas.drawText(ellipsizedValue, cx, cy + 12f * density, valuePaint)
        canvas.drawText("Tap to Press", cx, cy + 28f * density, hintPaint)
    }

    private fun drawGlowingArc(
        canvas: Canvas,
        startAngle: Float,
        sweepAngle: Float,
        accentArgb: Int,
        highlightArgb: Int,
        mainAlpha: Int,
        highlightAlpha: Int,
        glowScale: Float,
    ) {
        arcGlowPaint.strokeWidth = baseStrokePx + 13.5f * density
        arcGlowPaint.color = WearDeckHelper.withAlpha(accentArgb, (15f * glowScale).toInt())
        canvas.drawArc(arcBounds, startAngle, sweepAngle, false, arcGlowPaint)

        arcGlowPaint.strokeWidth = baseStrokePx + 8.5f * density
        arcGlowPaint.color = WearDeckHelper.withAlpha(accentArgb, (36f * glowScale).toInt())
        canvas.drawArc(arcBounds, startAngle, sweepAngle, false, arcGlowPaint)

        arcGlowPaint.strokeWidth = baseStrokePx + 4.0f * density
        arcGlowPaint.color = WearDeckHelper.withAlpha(accentArgb, (72f * glowScale).toInt())
        canvas.drawArc(arcBounds, startAngle, sweepAngle, false, arcGlowPaint)

        arcPaint.color = WearDeckHelper.withAlpha(accentArgb, mainAlpha)
        canvas.drawArc(arcBounds, startAngle, sweepAngle, false, arcPaint)

        arcCorePaint.color = WearDeckHelper.withAlpha(highlightArgb, highlightAlpha)
        canvas.drawArc(arcBounds, startAngle, sweepAngle, false, arcCorePaint)
    }

    private fun obtainSheenShader(cx: Float, cy: Float, radius: Float, accentArgb: Int): RadialGradient {
        val existing = cachedSheenShader
        if (existing != null &&
            cachedSheenAccent == accentArgb &&
            cachedSheenRadius == radius &&
            cachedSheenCx == cx &&
            cachedSheenCy == cy
        ) {
            return existing
        }
        val created = RadialGradient(
            cx,
            cy,
            radius.coerceAtLeast(1f),
            WearDeckHelper.withAlpha(accentArgb, 46),
            0,
            Shader.TileMode.CLAMP,
        )
        cachedSheenAccent = accentArgb
        cachedSheenRadius = radius
        cachedSheenCx = cx
        cachedSheenCy = cy
        cachedSheenShader = created
        return created
    }
}

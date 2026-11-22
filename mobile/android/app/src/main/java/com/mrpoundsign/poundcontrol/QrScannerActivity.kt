package com.mrpoundsign.poundcontrol

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Bundle
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.camera.core.Camera
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.MultiFormatReader
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.common.HybridBinarizer
import java.util.EnumMap
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

class QrScannerActivity : AppCompatActivity() {

    companion object {
        const val EXTRA_SCANNED_URL = "extra_scanned_url"
    }

    private lateinit var previewView: PreviewView
    private lateinit var permissionBox: LinearLayout
    private lateinit var torchBtn: Button
    private lateinit var cameraExecutor: ExecutorService

    private var camera: Camera? = null
    private var torchEnabled = false
    private val scanningActive = AtomicBoolean(true)

    private val qrReader = MultiFormatReader().apply {
        val hints = EnumMap<DecodeHintType, Any>(DecodeHintType::class.java)
        hints[DecodeHintType.POSSIBLE_FORMATS] = listOf(BarcodeFormat.QR_CODE)
        hints[DecodeHintType.TRY_HARDER] = true
        setHints(hints)
    }

    private val cameraPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (granted) {
            permissionBox.visibility = View.GONE
            previewView.visibility = View.VISIBLE
            startCamera()
        } else {
            previewView.visibility = View.GONE
            permissionBox.visibility = View.VISIBLE
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        applyImmersiveMode()

        cameraExecutor = Executors.newSingleThreadExecutor()

        val root = FrameLayout(this).apply {
            setBackgroundColor(Color.parseColor("#0A0A0C"))
        }

        previewView = PreviewView(this).apply {
            implementationMode = PreviewView.ImplementationMode.COMPATIBLE
        }
        root.addView(
            previewView,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )

        val overlay = buildOverlayUi()
        root.addView(
            overlay,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )

        permissionBox = buildPermissionFallbackUi().apply {
            visibility = View.GONE
        }
        root.addView(
            permissionBox,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT,
            ),
        )

        ViewCompat.setOnApplyWindowInsetsListener(overlay) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            val pad = dp(18)
            v.setPadding(bars.left + pad, bars.top + pad, bars.right + pad, bars.bottom + pad)
            insets
        }

        setContentView(root)

        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
            startCamera()
        } else {
            cameraPermissionLauncher.launch(Manifest.permission.CAMERA)
        }
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) {
            applyImmersiveMode()
        }
    }

    override fun onDestroy() {
        scanningActive.set(false)
        cameraExecutor.shutdownNow()
        super.onDestroy()
    }

    private fun buildOverlayUi(): FrameLayout {
        val overlay = FrameLayout(this)

        val prompt = TextView(this).apply {
            text = "Point camera at the QR code in PoundControl Settings -> Web Deck"
            setTextColor(Color.WHITE)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            typeface = Typeface.DEFAULT_BOLD
            gravity = Gravity.CENTER
            setBackgroundColor(Color.parseColor("#B30A0A0C"))
            setPadding(dp(14), dp(10), dp(14), dp(10))
        }
        overlay.addView(
            prompt,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.WRAP_CONTENT,
                FrameLayout.LayoutParams.WRAP_CONTENT,
                Gravity.TOP or Gravity.CENTER_HORIZONTAL,
            ),
        )

        val reticle = View(this).apply {
            background = GradientDrawable().apply {
                shape = GradientDrawable.RECTANGLE
                cornerRadius = dp(18).toFloat()
                setColor(Color.TRANSPARENT)
                setStroke(dp(3), Color.parseColor("#38BDF8"))
            }
        }
        val reticleSize = dp(240)
        overlay.addView(
            reticle,
            FrameLayout.LayoutParams(reticleSize, reticleSize, Gravity.CENTER),
        )

        val actions = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
        }

        torchBtn = makePillButton("Flash Off", "#1E293B", Color.WHITE) {
            val cam = camera ?: return@makePillButton
            if (cam.cameraInfo.hasFlashUnit()) {
                torchEnabled = !torchEnabled
                cam.cameraControl.enableTorch(torchEnabled)
                torchBtn.text = if (torchEnabled) "Flash On" else "Flash Off"
            }
        }.apply {
            visibility = View.GONE
        }
        actions.addView(torchBtn)

        val spacer = View(this)
        actions.addView(spacer, LinearLayout.LayoutParams(dp(12), 1))

        val cancelBtn = makePillButton("Cancel", "#141416", Color.WHITE) {
            setResult(RESULT_CANCELED)
            finish()
        }
        actions.addView(cancelBtn)

        overlay.addView(
            actions,
            FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.WRAP_CONTENT,
                FrameLayout.LayoutParams.WRAP_CONTENT,
                Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL,
            ),
        )
        return overlay
    }

    private fun buildPermissionFallbackUi(): LinearLayout {
        val box = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setBackgroundColor(Color.parseColor("#0A0A0C"))
            setPadding(dp(28), dp(28), dp(28), dp(28))
        }

        val msg = TextView(this).apply {
            text = "Camera permission is required to scan PoundControl's pairing QR code.\nYou can also close this screen and type your PC IP address manually."
            setTextColor(Color.parseColor("#E2E8F0"))
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            gravity = Gravity.CENTER
        }
        box.addView(msg)

        val retryBtn = makePillButton("Grant Camera Permission", "#38BDF8", Color.parseColor("#020617")) {
            cameraPermissionLauncher.launch(Manifest.permission.CAMERA)
        }.apply {
            val lp = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            )
            lp.topMargin = dp(18)
            layoutParams = lp
        }
        box.addView(retryBtn)

        val backBtn = makePillButton("Back to Manual Setup", "#1E293B", Color.WHITE) {
            setResult(RESULT_CANCELED)
            finish()
        }.apply {
            val lp = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            )
            lp.topMargin = dp(12)
            layoutParams = lp
        }
        box.addView(backBtn)

        return box
    }

    private fun startCamera() {
        val providerFuture = ProcessCameraProvider.getInstance(this)
        providerFuture.addListener({
            val provider = try {
                providerFuture.get()
            } catch (_: Exception) {
                return@addListener
            }

            val preview = Preview.Builder().build().apply {
                surfaceProvider = previewView.surfaceProvider
            }

            val analysis = ImageAnalysis.Builder()
                .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                .build()
                .apply {
                    setAnalyzer(cameraExecutor) { image ->
                        analyzeFrame(image)
                    }
                }

            try {
                provider.unbindAll()
                val bound = provider.bindToLifecycle(
                    this,
                    CameraSelector.DEFAULT_BACK_CAMERA,
                    preview,
                    analysis,
                )
                camera = bound
                torchBtn.visibility = if (bound.cameraInfo.hasFlashUnit()) View.VISIBLE else View.GONE
            } catch (_: Exception) {
                permissionBox.visibility = View.VISIBLE
            }
        }, ContextCompat.getMainExecutor(this))
    }

    private fun analyzeFrame(image: ImageProxy) {
        try {
            if (!scanningActive.get()) {
                return
            }
            val plane = image.planes.firstOrNull() ?: return
            val width = image.width
            val height = image.height
            val yBytes = extractTightYPlane(plane, width, height)

            val source = PlanarYUVLuminanceSource(
                yBytes,
                width,
                height,
                0,
                0,
                width,
                height,
                false,
            )

            val decodedText = tryDecode(source) ?: tryDecode(source.invert()) ?: return
            val normalized = DeckUrlValidator.normalizeUrl(decodedText) ?: return

            if (scanningActive.compareAndSet(true, false)) {
                runOnUiThread {
                    val data = Intent().putExtra(EXTRA_SCANNED_URL, normalized)
                    setResult(RESULT_OK, data)
                    finish()
                }
            }
        } finally {
            image.close()
        }
    }

    private fun tryDecode(source: com.google.zxing.LuminanceSource): String? {
        return try {
            val bitmap = BinaryBitmap(HybridBinarizer(source))
            qrReader.decodeWithState(bitmap).text
        } catch (_: Exception) {
            null
        } finally {
            qrReader.reset()
        }
    }

    private fun extractTightYPlane(plane: ImageProxy.PlaneProxy, width: Int, height: Int): ByteArray {
        val buf = plane.buffer
        val rowStride = plane.rowStride
        val pixelStride = plane.pixelStride
        val out = ByteArray(width * height)

        if (pixelStride == 1 && rowStride == width) {
            buf.get(out, 0, out.size)
            return out
        }

        val rowBuf = ByteArray(rowStride)
        for (row in 0 until height) {
            val rowStart = row * rowStride
            val remaining = buf.capacity() - rowStart
            if (remaining <= 0) {
                break
            }
            buf.position(rowStart)
            val toRead = minOf(rowStride, remaining)
            buf.get(rowBuf, 0, toRead)
            if (pixelStride == 1) {
                System.arraycopy(rowBuf, 0, out, row * width, minOf(width, toRead))
            } else {
                val outOffset = row * width
                for (col in 0 until width) {
                    val idx = col * pixelStride
                    if (idx < toRead) {
                        out[outOffset + col] = rowBuf[idx]
                    }
                }
            }
        }
        return out
    }

    private fun applyImmersiveMode() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        val controller = WindowInsetsControllerCompat(window, window.decorView)
        controller.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        controller.hide(WindowInsetsCompat.Type.systemBars())
    }

    private fun makePillButton(label: String, bgHex: String, fgColor: Int, onClick: () -> Unit): Button {
        return Button(this).apply {
            text = label
            isAllCaps = false
            setTextColor(fgColor)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            typeface = Typeface.DEFAULT_BOLD
            setPadding(dp(18), dp(10), dp(18), dp(10))
            background = GradientDrawable().apply {
                shape = GradientDrawable.RECTANGLE
                cornerRadius = dp(12).toFloat()
                setColor(Color.parseColor(bgHex))
                setStroke(dp(1), Color.parseColor("#475569"))
            }
            setOnClickListener { onClick() }
        }
    }

    private fun dp(value: Int): Int {
        return TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_DIP,
            value.toFloat(),
            resources.displayMetrics,
        ).toInt()
    }
}

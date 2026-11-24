package com.mrpoundsign.poundcontrol.wear

import android.app.PendingIntent
import android.content.Intent
import android.graphics.drawable.Icon
import androidx.wear.watchface.complications.data.ComplicationData
import androidx.wear.watchface.complications.data.ComplicationType
import androidx.wear.watchface.complications.data.MonochromaticImage
import androidx.wear.watchface.complications.data.MonochromaticImageComplicationData
import androidx.wear.watchface.complications.data.PlainComplicationText
import androidx.wear.watchface.complications.data.ShortTextComplicationData
import androidx.wear.watchface.complications.data.SmallImage
import androidx.wear.watchface.complications.data.SmallImageComplicationData
import androidx.wear.watchface.complications.data.SmallImageType
import androidx.wear.watchface.complications.datasource.ComplicationRequest
import androidx.wear.watchface.complications.datasource.SuspendingComplicationDataSourceService

/**
 * Watch Face Complication data source providing one-tap launch into [WearMainActivity].
 */
class PoundControlComplicationService : SuspendingComplicationDataSourceService() {

    override fun getPreviewData(type: ComplicationType): ComplicationData? {
        return buildComplicationData(type = type, tapAction = null)
    }

    override suspend fun onComplicationRequest(request: ComplicationRequest): ComplicationData? {
        return buildComplicationData(
            type = request.complicationType,
            tapAction = createLaunchPendingIntent(),
        )
    }

    private fun buildComplicationData(
        type: ComplicationType,
        tapAction: PendingIntent?,
    ): ComplicationData? {
        val label = getString(R.string.complication_label)
        val contentDescription = PlainComplicationText.Builder(label).build()
        val icon = Icon.createWithResource(this, R.drawable.ic_complication_deck)
        val monoImage = MonochromaticImage.Builder(icon).build()

        return when (type) {
            ComplicationType.MONOCHROMATIC_IMAGE -> {
                MonochromaticImageComplicationData.Builder(monoImage, contentDescription)
                    .setTapAction(tapAction)
                    .build()
            }
            ComplicationType.SMALL_IMAGE -> {
                val smallImage = SmallImage.Builder(icon, SmallImageType.ICON).build()
                SmallImageComplicationData.Builder(smallImage, contentDescription)
                    .setTapAction(tapAction)
                    .build()
            }
            ComplicationType.SHORT_TEXT -> {
                ShortTextComplicationData.Builder(
                    PlainComplicationText.Builder("Deck").build(),
                    contentDescription,
                )
                    .setMonochromaticImage(monoImage)
                    .setTapAction(tapAction)
                    .build()
            }
            else -> null
        }
    }

    private fun createLaunchPendingIntent(): PendingIntent {
        val intent = Intent(this, WearMainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or
                Intent.FLAG_ACTIVITY_CLEAR_TOP or
                Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        return PendingIntent.getActivity(
            this,
            0,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }
}

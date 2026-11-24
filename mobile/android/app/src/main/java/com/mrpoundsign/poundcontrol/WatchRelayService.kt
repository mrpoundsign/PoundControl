package com.mrpoundsign.poundcontrol

import com.google.android.gms.wearable.MessageEvent
import com.google.android.gms.wearable.WearableListenerService
import com.mrpoundsign.poundcontrol.protocol.WearProtocol

/**
 * Background WearableListenerService woken automatically by Play Services when the paired
 * Pixel Watch requests or sends deck actions over Bluetooth.
 */
class WatchRelayService : WearableListenerService() {
    override fun onMessageReceived(messageEvent: MessageEvent) {
        if (messageEvent.path.startsWith(WearProtocol.PATH_PREFIX)) {
            WatchRelayManager.handleWatchMessage(
                context = applicationContext,
                nodeId = messageEvent.sourceNodeId,
                path = messageEvent.path,
                data = messageEvent.data,
            )
        } else {
            super.onMessageReceived(messageEvent)
        }
    }
}

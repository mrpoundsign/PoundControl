package com.mrpoundsign.poundcontrol.protocol

/**
 * Shared Wear OS Data Layer message paths for phone <-> Pixel Watch relay.
 */
object WearProtocol {
    const val PATH_PREFIX = "/poundcontrol/watch"
    const val PATH_OPEN = "/poundcontrol/watch/open"
    const val PATH_CLOSE = "/poundcontrol/watch/close"
    const val PATH_PING = "/poundcontrol/watch/ping"
    const val PATH_CMD = "/poundcontrol/watch/cmd"
    const val PATH_SNAPSHOT = "/poundcontrol/watch/snapshot"
    const val PATH_STATUS = "/poundcontrol/watch/status"
}

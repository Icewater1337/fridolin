package local.plaudsync

/**
 * Plaud's user token, which lives 24 hours. [get] renews it once it is [RENEW_MS] old, by [now]:
 * SystemClock.elapsedRealtime in the app, which runs on while the phone sleeps.
 *
 * A timer cannot do this. A coroutine's delay on Android counts only the time the phone is
 * awake, and between check-ins it sleeps, so "renew in 12 hours" could come days late. Until
 * then every transcription failed with HTTP 401 and nothing reached Fridolin, while the pin's
 * recordings still came in over Bluetooth. Pure, so it is tested on the JVM.
 */
class PlaudToken(private val now: () -> Long, private val fetch: () -> String) {
    private var token: String? = null
    private var at = 0L

    /** The token, fetched anew when there is none or it is [RENEW_MS] old. Blocks while it fetches. */
    @Synchronized
    fun get(): String {
        token?.takeIf { now() - at < RENEW_MS }?.let { return it }
        val t = fetch()
        token = t
        at = now()
        return t
    }

    /** Plaud refused the token (HTTP 401): the next [get] fetches a new one. */
    @Synchronized
    fun invalidate() {
        token = null
    }

    companion object {
        /** Half the token's life, so one in use never runs out. */
        const val RENEW_MS = 12 * 3600_000L
    }
}

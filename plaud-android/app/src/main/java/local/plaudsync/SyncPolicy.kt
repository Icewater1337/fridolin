package local.plaudsync

/**
 * How the app reaches the NotePin (docs/PLAUD.md, "The pin's battery"). The pin tells the app
 * nothing while it is not connected: its advertisement carries who it is, never whether it
 * records or holds files. So a link that stays up costs the pin its sleep, and anything else
 * means checking in now and then.
 */
enum class SyncMode(val key: String) {
    /** The link stays up: a recording is off the pin seconds after it stops. */
    INSTANT("instant"),
    /** Checks in every [SyncPolicy.SMART_MINUTES], and at once on the events in [SyncPolicy.checkInOn]. */
    SMART("smart"),
    /** Checks in every N minutes, and when the app is opened or asked to. */
    INTERVAL("interval");

    companion object {
        fun of(key: String?) = entries.firstOrNull { it.key == key } ?: SMART
    }
}

/** What can start a check-in before its time. */
enum class Trigger(val key: String) {
    TIMER("timer"),
    /** The app's screen was opened. */
    OPENED("opened"),
    /** Fetch now, in the app or the notification: always, at once. */
    SYNC_NOW("sync_now"),
    /** The phone was plugged in. */
    CHARGING("charging"),
    /** The phone was unlocked. */
    UNLOCKED("unlocked"),
    /** The pin, away after check-ins that missed it, is seen again. */
    IN_RANGE("in_range"),
    /** The pin's advertisement changed while it was not connected (see [AdvertSignal]). */
    ADVERT("advert"),
    /** The sync setting changed. */
    SETTINGS("settings");

    companion object {
        fun of(key: String?) = entries.firstOrNull { it.key == key } ?: TIMER
    }
}

/** When to check in, how long to back off, and when to let the pin go. Pure, so it is tested on the JVM. */
object SyncPolicy {
    const val SMART_MINUTES = 10
    /** While voice mode is on a recorded question should not wait long: check in at least this often. */
    const val VOICE_MINUTES = 2
    const val MIN_MINUTES = 2
    const val MAX_MINUTES = 240
    const val DEFAULT_MINUTES = 30

    /** In instant mode a lost link is sought again after this, doubling up to [INSTANT_BACKOFF_CAP_MS]. */
    const val INSTANT_RETRY_MS = 30_000L
    const val INSTANT_BACKOFF_CAP_MS = 10 * 60_000L
    /** Check-ins that keep missing the pin double their wait up to this (or the interval, if longer). */
    const val BACKOFF_CAP_MS = 60 * 60_000L
    /** A link that dropped mid-sync (out of range for a moment, say) is tried again after this. */
    const val DROPPED_RETRY_MS = 30_000L

    /** Unlocking the phone checks in only if the last check-in is at least this old. */
    const val UNLOCK_GAP_MS = 5 * 60_000L
    /** Opening the app, plugging in or an advertisement change: at most one check-in per this. */
    const val EVENT_GAP_MS = 60_000L

    fun minutes(n: Int) = n.coerceIn(MIN_MINUTES, MAX_MINUTES)

    /** Time between check-ins while the pin is in range; null in instant mode, where the link stays. */
    fun interval(mode: SyncMode, minutes: Int, voice: Boolean): Long? {
        val m = when (mode) {
            SyncMode.INSTANT -> return null
            SyncMode.SMART -> SMART_MINUTES
            SyncMode.INTERVAL -> minutes(minutes)
        }
        return (if (voice) minOf(m, VOICE_MINUTES) else m) * 60_000L
    }

    /**
     * The wait before the next try after [misses] tries in a row found no pin (0: none missed):
     * the interval (instant mode: [INSTANT_RETRY_MS]), doubled for each miss after the first,
     * never more than the cap. The pin coming back in range ends the wait early ([Trigger.IN_RANGE]).
     */
    fun backoff(mode: SyncMode, minutes: Int, voice: Boolean, misses: Int): Long {
        val base = interval(mode, minutes, voice) ?: INSTANT_RETRY_MS
        val cap = if (mode == SyncMode.INSTANT) INSTANT_BACKOFF_CAP_MS else maxOf(BACKOFF_CAP_MS, base)
        val doublings = (misses - 1).coerceIn(0, 30)
        return if (base > cap shr doublings) cap else base shl doublings
    }

    /** Whether [trigger] starts a check-in now, the last one having started [sinceLastMs] ago. */
    fun checkInOn(trigger: Trigger, mode: SyncMode, sinceLastMs: Long): Boolean = when (trigger) {
        Trigger.TIMER, Trigger.SYNC_NOW, Trigger.IN_RANGE, Trigger.SETTINGS -> true
        Trigger.OPENED -> sinceLastMs >= EVENT_GAP_MS
        Trigger.CHARGING -> mode != SyncMode.INTERVAL && sinceLastMs >= EVENT_GAP_MS
        Trigger.UNLOCKED -> mode != SyncMode.INTERVAL && sinceLastMs >= UNLOCK_GAP_MS
        Trigger.ADVERT -> mode == SyncMode.SMART && sinceLastMs >= EVENT_GAP_MS
    }

    /**
     * Whether the link stays once nothing is left to download: in instant mode, and while the pin
     * records, since it is awake then anyway and the stop is seen at once.
     */
    fun stayConnected(mode: SyncMode, recording: Boolean, unpairing: Boolean) =
        mode == SyncMode.INSTANT || recording || unpairing

    /** Whether the app watches for the pin with its own low-power scan while it is not connected. */
    fun watch(mode: SyncMode, away: Boolean) = away || mode == SyncMode.SMART

    /** The positions at which two advertisements differ (a length change counts from the shorter end). */
    fun changedBytes(old: ByteArray, new: ByteArray): List<Int> =
        (0 until maxOf(old.size, new.size)).filter { i -> old.getOrNull(i) != new.getOrNull(i) }
}

/**
 * Whether a change in the pin's advertisement means something. Plaud documents no such signal,
 * so the app finds out: a check-in an advertisement change started is a hit when it found a new
 * recording or one in progress. After [TRIES] tries, the change counts only while at least half
 * of the tries were hits; after that it is no longer tried (until the pin is paired again).
 */
data class AdvertSignal(val tries: Int = 0, val hits: Int = 0) {
    val trusted get() = tries < TRIES || hits * 2 >= tries

    fun tried(hit: Boolean) = if (!trusted) this else AdvertSignal(tries + 1, hits + if (hit) 1 else 0)

    companion object {
        const val TRIES = 3
    }
}

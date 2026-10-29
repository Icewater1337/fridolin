package local.plaudsync

/**
 * Notices a sync that stands still. The service describes what it is doing as a [key]; the same
 * key for [limitMs] straight means nothing moves (a download that never reports back, a connect
 * the SDK never answers, a lost alarm). Pure, so it is tested on the JVM.
 */
class StallWatch(private val now: () -> Long) {
    private var last: String? = null
    private var since = 0L

    /** True once [key] has not changed for [limitMs]; the watch then starts over. */
    fun stalled(key: String, limitMs: Long): Boolean {
        if (key != last) {
            last = key
            since = now()
            return false
        }
        if (now() - since < limitMs) return false
        last = null
        return true
    }
}

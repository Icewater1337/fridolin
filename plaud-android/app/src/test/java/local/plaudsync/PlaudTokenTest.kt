package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Test

class PlaudTokenTest {
    private val hour = 3600_000L
    private var clock = 0L
    private var fetched = 0
    private val token = PlaudToken({ clock }) { "t${++fetched}" }

    @Test
    fun fetchesOnceAndKeepsItWhileItIsYoung() {
        assertEquals("t1", token.get())
        clock += 11 * hour
        assertEquals("t1", token.get())
        assertEquals(1, fetched)
    }

    // The phone slept through the night: no timer ran, but the token aged all the same.
    // The next transcription must not go to Plaud with a token that ran out.
    @Test
    fun renewsAfterTheTimeHasPassedEvenWithNoTimerRunning() {
        assertEquals("t1", token.get())
        clock += 25 * hour
        assertEquals("t2", token.get())
        clock += 12 * hour
        assertEquals("t3", token.get())
    }

    @Test
    fun aTokenPlaudRefusedIsFetchedAgain() {
        assertEquals("t1", token.get())
        token.invalidate()
        assertEquals("t2", token.get())
    }

    @Test
    fun aFailedFetchIsTriedAgainNextTime() {
        var fail = true
        val flaky = PlaudToken({ clock }) { if (fail) error("offline") else "ok" }
        runCatching { flaky.get() }
        fail = false
        assertEquals("ok", flaky.get())
    }
}

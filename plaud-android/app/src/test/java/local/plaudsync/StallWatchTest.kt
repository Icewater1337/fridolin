package local.plaudsync

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class StallWatchTest {
    private var t = 0L
    private val w = StallWatch { t }

    @Test
    fun theSameStateForTheLimitIsAStall() {
        assertFalse(w.stalled("connected", 10))
        t = 9
        assertFalse(w.stalled("connected", 10))
        t = 10
        assertTrue(w.stalled("connected", 10))
    }

    @Test
    fun aChangeOfStateStartsTheClockAgain() {
        w.stalled("exporting 1", 10)
        t = 8
        assertFalse(w.stalled("exporting 2", 10))
        t = 17
        assertFalse(w.stalled("exporting 2", 10))
    }

    @Test
    fun afterAStallItWaitsAnotherFullLimit() {
        w.stalled("x", 10)
        t = 10
        assertTrue(w.stalled("x", 10))
        assertFalse(w.stalled("x", 10))
        t = 19
        assertFalse(w.stalled("x", 10))
        t = 20
        assertTrue(w.stalled("x", 10))
    }
}

package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SyncPolicyTest {
    private val min = 60_000L

    @Test
    fun modesReadBackAndUnknownIsSmart() {
        SyncMode.entries.forEach { assertEquals(it, SyncMode.of(it.key)) }
        assertEquals(SyncMode.SMART, SyncMode.of(null))
        assertEquals(SyncMode.SMART, SyncMode.of("often"))
        Trigger.entries.forEach { assertEquals(it, Trigger.of(it.key)) }
        assertEquals(Trigger.TIMER, Trigger.of(null))
    }

    @Test
    fun intervalPerMode() {
        assertNull(SyncPolicy.interval(SyncMode.INSTANT, 30, voice = false))
        assertNull(SyncPolicy.interval(SyncMode.INSTANT, 30, voice = true))
        assertEquals(10 * min, SyncPolicy.interval(SyncMode.SMART, 30, voice = false))
        assertEquals(30 * min, SyncPolicy.interval(SyncMode.INTERVAL, 30, voice = false))
    }

    @Test
    fun minutesAreClamped() {
        assertEquals(2 * min, SyncPolicy.interval(SyncMode.INTERVAL, 0, voice = false))
        assertEquals(240 * min, SyncPolicy.interval(SyncMode.INTERVAL, 10_000, voice = false))
    }

    @Test
    fun voiceModeChecksInAtLeastEveryTwoMinutes() {
        assertEquals(2 * min, SyncPolicy.interval(SyncMode.SMART, 30, voice = true))
        assertEquals(2 * min, SyncPolicy.interval(SyncMode.INTERVAL, 60, voice = true))
        assertEquals(2 * min, SyncPolicy.interval(SyncMode.INTERVAL, 2, voice = true))
    }

    @Test
    fun backoffDoublesFromTheIntervalUpToAnHour() {
        val waits = (0..6).map { SyncPolicy.backoff(SyncMode.SMART, 30, voice = false, misses = it) / min }
        assertEquals(listOf(10L, 10, 20, 40, 60, 60, 60), waits)
    }

    @Test
    fun backoffNeverWaitsLessThanTheInterval() {
        assertEquals(240 * min, SyncPolicy.backoff(SyncMode.INTERVAL, 240, voice = false, misses = 1))
        assertEquals(240 * min, SyncPolicy.backoff(SyncMode.INTERVAL, 240, voice = false, misses = 9))
    }

    @Test
    fun backoffInInstantModeStartsAtThirtySecondsAndStopsAtTen() {
        val waits = (1..7).map { SyncPolicy.backoff(SyncMode.INSTANT, 30, voice = false, misses = it) / 1000 }
        assertEquals(listOf(30L, 60, 120, 240, 480, 600, 600), waits)
    }

    @Test
    fun backoffInVoiceModeStartsShortButStillBacksOff() {
        val waits = (1..7).map { SyncPolicy.backoff(SyncMode.SMART, 30, voice = true, misses = it) / min }
        assertEquals(listOf(2L, 4, 8, 16, 32, 60, 60), waits)
    }

    @Test
    fun backoffDoesNotOverflow() {
        SyncMode.entries.forEach { mode ->
            listOf(31, 62, 1000, Int.MAX_VALUE).forEach { misses ->
                val w = SyncPolicy.backoff(mode, SyncPolicy.MAX_MINUTES, voice = false, misses = misses)
                assertTrue("$mode $misses: $w", w in 30_000L..SyncPolicy.MAX_MINUTES * min)
            }
        }
    }

    @Test
    fun syncNowTimerAndSettingsAlwaysCheckIn() {
        SyncMode.entries.forEach { mode ->
            listOf(Trigger.SYNC_NOW, Trigger.TIMER, Trigger.SETTINGS, Trigger.IN_RANGE).forEach {
                assertTrue("$mode $it", SyncPolicy.checkInOn(it, mode, 0))
            }
        }
    }

    @Test
    fun openingTheAppChecksInOncePerMinute() {
        SyncMode.entries.forEach { mode ->
            assertFalse(SyncPolicy.checkInOn(Trigger.OPENED, mode, 59_000))
            assertTrue(SyncPolicy.checkInOn(Trigger.OPENED, mode, 60_000))
        }
    }

    @Test
    fun unlockAndChargingAreSmartEventsNotEveryNMinutes() {
        assertTrue(SyncPolicy.checkInOn(Trigger.CHARGING, SyncMode.SMART, min))
        assertFalse(SyncPolicy.checkInOn(Trigger.CHARGING, SyncMode.INTERVAL, 60 * min))
        assertTrue(SyncPolicy.checkInOn(Trigger.UNLOCKED, SyncMode.SMART, 5 * min))
        assertFalse(SyncPolicy.checkInOn(Trigger.UNLOCKED, SyncMode.SMART, 4 * min))
        assertFalse(SyncPolicy.checkInOn(Trigger.UNLOCKED, SyncMode.INTERVAL, 60 * min))
    }

    @Test
    fun advertChangesCountOnlyInSmartMode() {
        assertTrue(SyncPolicy.checkInOn(Trigger.ADVERT, SyncMode.SMART, min))
        assertFalse(SyncPolicy.checkInOn(Trigger.ADVERT, SyncMode.SMART, 30_000))
        assertFalse(SyncPolicy.checkInOn(Trigger.ADVERT, SyncMode.INTERVAL, 60 * min))
        assertFalse(SyncPolicy.checkInOn(Trigger.ADVERT, SyncMode.INSTANT, 60 * min))
    }

    @Test
    fun theLinkStaysOnlyInInstantModeWhileRecordingOrToUnpair() {
        assertTrue(SyncPolicy.stayConnected(SyncMode.INSTANT, recording = false, unpairing = false))
        assertFalse(SyncPolicy.stayConnected(SyncMode.SMART, recording = false, unpairing = false))
        assertFalse(SyncPolicy.stayConnected(SyncMode.INTERVAL, recording = false, unpairing = false))
        assertTrue(SyncPolicy.stayConnected(SyncMode.SMART, recording = true, unpairing = false))
        assertTrue(SyncPolicy.stayConnected(SyncMode.INTERVAL, recording = false, unpairing = true))
    }

    @Test
    fun watchWhenAwayOrSmart() {
        assertTrue(SyncPolicy.watch(SyncMode.SMART, away = false))
        assertFalse(SyncPolicy.watch(SyncMode.INTERVAL, away = false))
        assertFalse(SyncPolicy.watch(SyncMode.INSTANT, away = false))
        SyncMode.entries.forEach { assertTrue(SyncPolicy.watch(it, away = true)) }
    }

    @Test
    fun changedBytes() {
        assertEquals(emptyList<Int>(), SyncPolicy.changedBytes(byteArrayOf(1, 2), byteArrayOf(1, 2)))
        assertEquals(listOf(1), SyncPolicy.changedBytes(byteArrayOf(1, 2, 3), byteArrayOf(1, 9, 3)))
        assertEquals(listOf(2, 3), SyncPolicy.changedBytes(byteArrayOf(1, 2), byteArrayOf(1, 2, 3, 4)))
    }

    @Test
    fun advertSignalIsTrustedUntilItProvesNoise() {
        var s = AdvertSignal()
        assertTrue(s.trusted)
        s = s.tried(false).tried(false)
        assertTrue(s.trusted) // two tries are not enough to tell
        s = s.tried(false)
        assertFalse(s.trusted)
        assertEquals(s, s.tried(true)) // no longer tried, so it never comes back by chance
    }

    @Test
    fun advertSignalThatHitsStaysTrusted() {
        var s = AdvertSignal()
        repeat(10) { s = s.tried(it % 2 == 0) }
        assertTrue(s.trusted)
        assertEquals(AdvertSignal(10, 5), s)
        s = s.tried(false)
        assertFalse(s.trusted)
    }
}

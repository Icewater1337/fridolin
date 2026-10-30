package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdatePolicyTest {
    private val mine = "ab".repeat(32)
    private fun rel(code: Long, cert: String = mine) = Release(code, "0.7", "cd".repeat(32), cert, 100)

    @Test
    fun asksAtMostEveryTenMinutesUnlessOpened() {
        assertTrue("never asked", UpdatePolicy.due(0, 1_800_000_000_000, forced = false))
        assertFalse(UpdatePolicy.due(1_000, 1_000 + UpdatePolicy.CHECK_EVERY_MS - 1, forced = false))
        assertTrue(UpdatePolicy.due(1_000, 1_000 + UpdatePolicy.CHECK_EVERY_MS, forced = false))
        assertTrue(UpdatePolicy.due(1_000, 1_001, forced = true))
        assertTrue("a clock set back", UpdatePolicy.due(5_000, 1_000, forced = false))
    }

    @Test
    fun fetchesOnlyANewerBuildSignedWithItsOwnKey() {
        val st = UpdateState()
        assertEquals(UpdatePolicy.Fetch.NONE, UpdatePolicy.fetch(8, mine, null, st))
        assertEquals(UpdatePolicy.Fetch.NONE, UpdatePolicy.fetch(8, mine, rel(8), st))
        assertEquals(UpdatePolicy.Fetch.NONE, UpdatePolicy.fetch(9, mine, rel(8), st))
        assertEquals(UpdatePolicy.Fetch.DOWNLOAD, UpdatePolicy.fetch(8, mine, rel(9), st))
        assertEquals(UpdatePolicy.Fetch.OTHER_KEY, UpdatePolicy.fetch(8, mine, rel(9, cert = "ef".repeat(32)), st))
        assertEquals("fetched already", UpdatePolicy.Fetch.NONE, UpdatePolicy.fetch(8, mine, rel(9), UpdateState(ready = 9)))
        assertEquals("failed its checks", UpdatePolicy.Fetch.NONE, UpdatePolicy.fetch(8, mine, rel(9), UpdateState(bad = 9)))
        assertEquals("a newer one than the bad one", UpdatePolicy.Fetch.DOWNLOAD, UpdatePolicy.fetch(8, mine, rel(10), UpdateState(bad = 9)))
    }

    @Test
    fun installsWhatWasFetchedOnceAndWaitsForTheOwnersTap() {
        val now = 1_000_000_000L
        assertFalse("nothing fetched", UpdatePolicy.install(8, UpdateState(), now, forced = false))
        assertFalse("installed already", UpdatePolicy.install(9, UpdateState(ready = 9), now, forced = false))
        assertTrue(UpdatePolicy.install(8, UpdateState(ready = 9), now, forced = false))
        assertFalse("handed to Android a minute ago", UpdatePolicy.install(8, UpdateState(ready = 9, installing = 9, installingAt = now - 60_000), now, forced = false))
        assertTrue("handed over long ago", UpdatePolicy.install(8, UpdateState(ready = 9, installing = 9, installingAt = now - UpdatePolicy.INSTALLING_MS), now, forced = false))
        assertFalse("waits for the tap", UpdatePolicy.install(8, UpdateState(ready = 9, confirm = 9), now, forced = false))
        assertTrue("the app is opened", UpdatePolicy.install(8, UpdateState(ready = 9, confirm = 9), now, forced = true))
        assertTrue("a newer build than the one waiting", UpdatePolicy.install(8, UpdateState(ready = 10, confirm = 9), now, forced = false))
        assertFalse("refused an hour ago", UpdatePolicy.install(8, UpdateState(ready = 9, failed = 9, failedAt = now - 3600_000), now, forced = false))
        assertTrue("refused long ago", UpdatePolicy.install(8, UpdateState(ready = 9, failed = 9, failedAt = now - UpdatePolicy.RETRY_MS), now, forced = false))
    }
}

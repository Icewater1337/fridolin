package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class VoiceQueueTest {
    private val transcript = "[00:00 - 00:04] Speaker 1: Kurzes Update zum Budget.\n[00:04 - 00:09] Speaker 2: Wir verschieben die Bestellung.\n"

    @Test
    fun aRecordingKeepsItsNameAndItsWholeTranscript() {
        val content = VoiceQueue.pin("2026-10-18_10-00-00", transcript)
        assertEquals(VoiceQueue.Pin("2026-10-18_10-00-00", transcript), VoiceQueue.readPin(content))
    }

    @Test
    fun aFileThatHoldsNoRecordingIsNone() {
        assertNull(VoiceQueue.readPin("Was steht morgen an?"))
        assertNull(VoiceQueue.readPin("Was steht morgen an?\nund übermorgen?"))
        assertNull(VoiceQueue.readPin("../../2026-10-18_10-00-00\n$transcript"))
        assertNull(VoiceQueue.readPin(""))
    }

    @Test(expected = IllegalArgumentException::class)
    fun onlyARecordingsNameIsOne() {
        VoiceQueue.pin("notes", transcript)
    }

    @Test
    fun aRefusalForGoodIsA4xxThatTryingAgainWouldNotChange() {
        for (code in listOf(400, 403, 404, 413, 415, 422)) assertTrue("$code", VoiceQueue.refused(code))
        // Unpaired (the next try pairs), a timeout, too many failures; Fridolin or the site down.
        for (code in listOf(401, 408, 429, 500, 502, 503, 504, 200, 302)) assertFalse("$code", VoiceQueue.refused(code))
    }
}

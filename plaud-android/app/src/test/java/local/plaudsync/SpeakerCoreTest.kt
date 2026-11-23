package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Locale

class SpeakerCoreTest {
    /** A voice that records what it was asked; the test says when it is ready and when a piece ends. */
    private class FakeVoice : SpeechVoice {
        override var onReady: (Boolean) -> Unit = {}
        override var onDone: (String) -> Unit = {}
        override var onError: (String, Int) -> Unit = { _, _ -> }
        var accepts = true
        val said = mutableListOf<String>()
        var lastId: String? = null
        var stops = 0
        var shut = false

        override fun speak(text: String, locale: Locale, id: String): Boolean {
            if (!accepts) return false
            said += text
            lastId = id
            return true
        }

        override fun stop() {
            stops++
        }

        override fun supports(locale: Locale) = true
        override fun shutdown() {
            shut = true
        }

        fun ready(ok: Boolean = true) = onReady(ok)
        fun finish() = onDone(lastId!!)
        fun fail(code: Int = -3) = onError(lastId!!, code)
    }

    private class FakeFocus : FocusPort {
        var grant = true
        var held = false
        var requests = 0
        var abandons = 0
        override fun request(): Boolean {
            requests++
            if (grant) held = true
            return grant
        }

        override fun abandon() {
            abandons++
            held = false
        }
    }

    /** A clock that moves only when the test says so. */
    private class FakeClock : Scheduler {
        var now = 0L
        private val tasks = mutableListOf<Triple<Long, Int, () -> Unit>>()
        private var seq = 0
        override fun after(delayMs: Long, task: () -> Unit): Cancellable {
            val t = Triple(now + delayMs, seq++, task)
            tasks += t
            return Cancellable { tasks.remove(t) }
        }

        fun advance(ms: Long) {
            val until = now + ms
            while (true) {
                val next = tasks.filter { it.first <= until }.minWithOrNull(compareBy({ it.first }, { it.second })) ?: break
                tasks.remove(next)
                now = next.first
                next.third()
            }
            now = until
        }
    }

    private val voices = mutableListOf<FakeVoice>()
    private val focus = FakeFocus()
    private val clock = FakeClock()
    private var locked = false
    private var call = false
    private val log = mutableListOf<String>()
    private var changes = 0
    private val core = SpeakerCore(
        newVoice = { FakeVoice().also { voices += it } },
        focus = focus,
        later = clock,
        lockedQuiet = { locked },
        inCall = { call },
        log = { log += it },
        changed = { changes++ },
    )
    private val voice get() = voices.last()

    private fun logged(part: String) = log.any { part in it }

    @Test
    fun anAnswerIsSaidSentenceBySentenceWithFocusOnlyWhileItSpeaks() {
        voice.ready()
        assertFalse(focus.held)
        core.say("First one. Second one.")
        assertTrue(core.busy)
        assertTrue(focus.held)
        assertEquals(listOf("First one."), voice.said)
        voice.finish()
        assertEquals(listOf("First one.", "Second one."), voice.said)
        assertEquals(1, focus.requests) // kept between the sentences of one answer
        voice.finish()
        assertFalse(core.busy)
        assertFalse(focus.held)
        assertEquals(1, focus.abandons)
        assertTrue(logged("answer spoken"))
    }

    @Test
    fun answersWaitForTheEngineAndGoOneAfterTheOther() {
        core.say("One.")
        core.say("Two.")
        assertEquals(0, focus.requests) // nothing to say yet: no focus
        voice.ready()
        assertEquals(listOf("One."), voice.said)
        voice.finish()
        assertEquals(listOf("One.", "Two."), voice.said)
        voice.finish()
        assertFalse(core.busy)
        assertFalse(focus.held)
    }

    @Test
    fun aSpeakThatFailsLetsGoOfTheFocusAndTriesAgainOnAFreshEngine() {
        val first = voice
        first.ready()
        first.accepts = false
        core.say("Hello there.")
        assertFalse(focus.held)
        assertTrue(first.shut)
        assertEquals(2, voices.size)
        assertTrue(logged("did not take the sentence"))
        assertTrue(core.busy)
        voice.ready()
        assertEquals(listOf("Hello there."), voice.said)
        assertTrue(focus.held)
        voice.finish()
        assertFalse(core.busy)
        assertFalse(focus.held)
    }

    @Test
    fun afterTwoFailedTriesTheAnswerIsDroppedAndTheNextOneGoesOn() {
        voice.ready()
        voice.accepts = false
        core.say("Broken answer.")
        core.say("Next answer.")
        // Try 2, on a fresh engine, fails too: the answer is dropped.
        voice.accepts = false
        voice.ready()
        assertTrue(logged("dropped the answer after 2 tries"))
        assertFalse(focus.held)
        assertTrue(core.busy)
        // The third engine comes after a pause, and says the next answer.
        assertEquals(2, voices.size)
        clock.advance(SpeakerCore.backoffMs(2))
        assertEquals(3, voices.size)
        voice.ready()
        assertEquals(listOf("Next answer."), voice.said)
        voice.finish()
        assertFalse(core.busy)
        assertFalse(focus.held)
    }

    @Test
    fun anErrorFromTheEngineCountsAsAFailedTry() {
        voice.ready()
        core.say("One sentence.")
        voice.fail(-4)
        assertFalse(focus.held)
        assertTrue(logged("error -4"))
        assertEquals(2, voices.size)
        voice.ready()
        assertEquals(listOf("One sentence."), voice.said)
    }

    @Test
    fun anOldEnginesLateCallbacksAreIgnored() {
        val first = voice
        first.ready()
        core.say("One. Two.")
        val id = first.lastId!!
        first.fail()
        voice.ready()
        first.onDone(id) // too late: the second engine says it now
        assertEquals(listOf("One."), voice.said)
        assertTrue(focus.held)
    }

    @Test
    fun noWordFromTheEngineTimesOutAndLetsGoOfTheFocus() {
        voice.ready()
        core.say("Five words in this one.")
        assertTrue(focus.held)
        clock.advance(SpeakerCore.timeoutMs("Five words in this one.") - 1)
        assertTrue(focus.held)
        clock.advance(1)
        assertFalse(focus.held)
        assertTrue(logged("said nothing back"))
        assertEquals(2, voices.size)
        voice.ready()
        assertEquals(listOf("Five words in this one."), voice.said)
    }

    @Test
    fun theTimeoutGrowsWithTheSentenceButStaysBounded() {
        assertEquals(15_500L, SpeakerCore.timeoutMs("one"))
        assertEquals(20_000L, SpeakerCore.timeoutMs("a b c d e f g h i j"))
        assertEquals(120_000L, SpeakerCore.timeoutMs("word ".repeat(1000)))
    }

    @Test
    fun anEngineThatCannotStartIsMadeAnewLaterNotInATightLoop() {
        voice.ready(false)
        assertTrue(voices[0].shut)
        assertEquals(1, voices.size)
        assertTrue(logged("not available"))
        clock.advance(1_999)
        assertEquals(1, voices.size)
        clock.advance(1)
        assertEquals(2, voices.size)
        voice.ready(false)
        clock.advance(2_000)
        assertEquals(2, voices.size) // the second failure in a row waits longer
        clock.advance(8_000)
        assertEquals(3, voices.size)
    }

    @Test
    fun aNewAnswerMakesANewEngineAtOnce() {
        voice.ready(false)
        voice.ready(false) // a late second call changes nothing
        assertEquals(1, voices.size)
        core.say("Are you there?")
        assertEquals(2, voices.size)
        voice.ready()
        assertEquals(listOf("Are you there?"), voice.said)
    }

    @Test
    fun anEngineThatNeverSaysWhetherItIsReadyCountsAsFailed() {
        core.say("Hello.")
        clock.advance(SpeakerCore.READY_TIMEOUT_MS)
        assertTrue(logged("did not start"))
        assertTrue(voices[0].shut)
        clock.advance(2_000)
        assertEquals(2, voices.size)
        voice.ready()
        assertEquals(listOf("Hello."), voice.said)
    }

    @Test
    fun losingTheFocusForGoodLetsGoAndDoesNotTakeItBack() {
        voice.ready()
        core.say("First sentence. Second sentence.")
        voice.finish()
        assertEquals(1, focus.requests)
        core.focusLost() // a video starts
        assertFalse(focus.held)
        assertEquals(1, voice.stops)
        assertTrue(core.busy)
        assertTrue(logged("another app took the audio focus"))
        clock.advance(10 * 60_000L)
        core.recheck() // a call ends, say
        core.focusGained()
        assertEquals(1, focus.requests)
        assertFalse(focus.held)
        // The unlock goes on from the sentence it was in.
        core.resume()
        assertEquals(2, focus.requests)
        assertEquals(listOf("First sentence.", "Second sentence.", "Second sentence."), voice.said)
        voice.finish()
        assertFalse(core.busy)
        assertFalse(focus.held)
    }

    @Test
    fun aNewAnswerAfterLosingTheFocusForGoodSpeaksAgain() {
        voice.ready()
        core.say("Interrupted.")
        core.focusLost()
        assertFalse(focus.held)
        core.say("Fresh.")
        assertTrue(focus.held)
        assertEquals(listOf("Interrupted.", "Interrupted."), voice.said)
        voice.finish()
        assertEquals("Fresh.", voice.said.last())
    }

    @Test
    fun skipAfterLosingTheFocusGoesOnWithTheNextAnswer() {
        voice.ready()
        core.say("Interrupted.")
        core.say("Next.")
        core.focusLost()
        core.skip()
        assertTrue(focus.held)
        assertEquals("Next.", voice.said.last())
    }

    @Test
    fun aShortLossPausesAndGoesOnWhenTheFocusIsBack() {
        voice.ready()
        core.say("One. Two.")
        core.focusLostBriefly() // a call rings
        assertEquals(1, voice.stops)
        assertTrue(core.busy)
        assertEquals(listOf("One."), voice.said)
        core.recheck()
        assertEquals(listOf("One."), voice.said)
        core.focusGained()
        assertEquals(listOf("One.", "One."), voice.said)
        assertEquals(1, focus.requests)
        voice.finish()
        voice.finish()
        assertFalse(focus.held)
    }

    @Test
    fun aShortLossWhoseGainNeverComesIsClearedByALossForGoodOrAnUnlock() {
        voice.ready()
        core.say("One.")
        core.focusLostBriefly()
        core.focusLost()
        assertFalse(focus.held)
        core.resume()
        assertTrue(focus.held)
        assertEquals(listOf("One.", "One."), voice.said)

        voice.finish()
        core.say("Two.")
        core.focusLostBriefly()
        assertTrue(focus.held) // still in line for the focus to come back
        core.resume()
        assertEquals(listOf("One.", "One.", "Two.", "Two."), voice.said)
        assertTrue(focus.held)
    }

    @Test
    fun whileLockedAnAnswerIsHeldWithoutFocusAndSpokenAfterTheUnlock() {
        voice.ready()
        locked = true
        core.say("Secret plans.")
        assertTrue(core.held)
        assertTrue(core.busy)
        assertEquals(0, focus.requests)
        assertTrue(voice.said.isEmpty())
        assertTrue(logged("the phone is locked"))
        core.recheck()
        assertEquals(1, log.count { "the phone is locked" in it }) // once, not each time it looks
        locked = false
        core.resume()
        assertFalse(core.held)
        assertTrue(focus.held)
        assertEquals(listOf("Secret plans."), voice.said)
        voice.finish()
        assertFalse(core.busy)
        assertFalse(core.held)
    }

    @Test
    fun aLockBetweenSentencesHoldsTheRestAndLetsGoOfTheFocus() {
        voice.ready()
        core.say("One. Two.")
        locked = true
        voice.finish()
        assertTrue(core.held)
        assertFalse(focus.held)
        locked = false
        core.resume()
        assertEquals(listOf("One.", "Two."), voice.said)
    }

    @Test
    fun duringACallNothingStartsAndNoFocusIsHeld() {
        voice.ready()
        call = true
        core.say("After the call.")
        assertEquals(0, focus.requests)
        assertTrue(logged("a call is on"))
        call = false
        core.recheck()
        assertEquals(listOf("After the call."), voice.said)
    }

    @Test
    fun aDeniedFocusLeavesTheAnswerQueuedWithoutTryingAgainAndAgain() {
        voice.ready()
        focus.grant = false
        core.say("Later.")
        assertEquals(1, focus.requests)
        assertTrue(core.busy)
        assertTrue(logged("denied"))
        clock.advance(60 * 60_000L)
        assertEquals(1, focus.requests)
        focus.grant = true
        core.resume()
        assertEquals(listOf("Later."), voice.said)
    }

    @Test
    fun stopSilencesAndDropsTheQueue() {
        voice.ready()
        core.say("One. Two.")
        core.say("Three.")
        val before = changes
        core.stop()
        assertTrue(changes > before)
        assertFalse(core.busy)
        assertFalse(focus.held)
        assertEquals(1, voice.stops)
        voice.finish() // a late callback for what was stopped
        assertEquals(listOf("One."), voice.said)
        clock.advance(10 * 60_000L)
        assertEquals(1, voices.size)
    }

    @Test
    fun skipGoesOnWithTheNextAnswer() {
        voice.ready()
        core.say("One. Two.")
        core.say("Three.")
        core.skip()
        assertEquals(listOf("One.", "Three."), voice.said)
        assertTrue(focus.held)
        voice.finish()
        assertFalse(core.busy)
        assertFalse(focus.held)
    }

    @Test
    fun aBlankAnswerIsNotQueued() {
        voice.ready()
        core.say("```\n```")
        assertFalse(core.busy)
        assertEquals(0, focus.requests)
    }

    @Test
    fun shutdownLetsGoOfEverything() {
        voice.ready()
        core.say("One.")
        core.shutdown()
        assertFalse(focus.held)
        assertTrue(voice.shut)
        core.say("Two.")
        clock.advance(10 * 60_000L)
        assertEquals(1, voices.size)
        assertEquals(listOf("One."), voice.said)
    }

    @Test
    fun theLogNeverHoldsTheAnswer() {
        voice.ready()
        voice.accepts = false
        core.say("The secret number is forty two.")
        voice.ready(false)
        clock.advance(10 * 60_000L)
        locked = true
        core.say("Another secret.")
        assertNull(log.firstOrNull { "secret" in it.lowercase() || "forty" in it })
    }
}

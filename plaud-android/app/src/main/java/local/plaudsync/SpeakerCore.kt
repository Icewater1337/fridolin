package local.plaudsync

import java.util.Locale

/** Audio focus as the speaker uses it: transient, letting other apps duck (Android's AudioManager). */
interface FocusPort {
    /** Asks for focus; true when it was granted. */
    fun request(): Boolean
    fun abandon()
}

/** Something [Scheduler.after] will run, until it is cancelled. */
fun interface Cancellable {
    fun cancel()
}

/** Runs a task later on the speaker's thread (a Handler on the phone, a fake clock in tests). */
fun interface Scheduler {
    fun after(delayMs: Long, task: () -> Unit): Cancellable
}

/**
 * What [Speaker] decides, without Android: the answers waiting, the one being said and where in
 * it, audio focus, and the text-to-speech engine's health. The Android parts come in through
 * [SpeechVoice], [FocusPort] and [Scheduler], so it is tested on the JVM (SpeakerCoreTest).
 *
 * - Audio focus is asked for right before a sentence is said and given up as soon as nothing is
 *   being said: the queue is empty, an answer is held while the phone is locked, a call is on, an
 *   app took the focus, or the engine failed. Other apps are ducked only while it really speaks.
 * - Another app that takes the focus for good (a video, music) stops it, and it does not take it
 *   back on its own: the answer keeps its place, from the sentence it was in, until [resume] (an
 *   unlock), a new answer ([say]) or [skip]. A short loss (a call ringing, a navigation prompt)
 *   pauses it until the focus comes back ([focusGained]), or [resume].
 * - The engine has to say each sentence: when it does not take one, reports an error, or says
 *   nothing within [timeoutMs], the engine is shut down and made anew, and the sentence is tried
 *   again on it. After [MAX_TRIES] failures the answer is dropped and the next one goes on. An
 *   engine that does not start is made anew after [backoffMs], or at once for a new answer.
 *
 * Every reason an answer is not said now goes to [log], never the answer's text. Use it on one
 * thread only: the callbacks of the voice and the scheduler come on that thread.
 */
class SpeakerCore(
    private val newVoice: () -> SpeechVoice,
    private val focus: FocusPort,
    private val later: Scheduler,
    /** The phone is locked and answers may not be spoken while it is ([VoicePrefs.speakLocked]). */
    private val lockedQuiet: () -> Boolean,
    /** A call rings or is on. */
    private val inCall: () -> Boolean,
    private val log: (String) -> Unit,
    /** Something [held] or [busy] shows changed: the notification follows. */
    private val changed: () -> Unit,
) {
    companion object {
        private val space = Regex("""\s+""")

        /** Tries of one sentence, each on a fresh engine, before its answer is dropped. */
        const val MAX_TRIES = 2

        /** How long a new engine may take to say whether it can speak. */
        const val READY_TIMEOUT_MS = 30_000L

        /**
         * How long the engine may take to say a sentence of [text] before it counts as failed: 15 s
         * to start (a voice may have to load) and half a second per word, which is slower than
         * any voice speaks, but at most 2 minutes. Fridolin sends at most about 60 words to be
         * spoken, so a sentence takes well under that.
         */
        fun timeoutMs(text: String): Long {
            val words = text.split(space).count { it.isNotEmpty() }
            return minOf(15_000L + 500L * words, 120_000L)
        }

        /** How long to wait before a new engine after [failures] failures in a row: none after the first. */
        fun backoffMs(failures: Int): Long = when {
            failures <= 1 -> 0L
            failures == 2 -> 2_000L
            failures == 3 -> 10_000L
            failures == 4 -> 60_000L
            else -> 5 * 60_000L
        }
    }

    private enum class Engine { STARTING, READY, DOWN }

    /** Why it does not speak although it could: another app has the audio focus. */
    private enum class Yield { NONE, BRIEFLY, FOR_GOOD }

    private var voice: SpeechVoice? = null
    private var engine = Engine.DOWN
    private var readyTimer: Cancellable? = null
    private var restartTimer: Cancellable? = null
    /** Failures (of the engine or a sentence) since a sentence was last said. */
    private var failures = 0

    private val queue = ArrayDeque<String>()
    private var answer: String? = null
    private var current: List<String>? = null
    private var index = 0
    private var locale: Locale? = null
    /** Failed tries of the sentence at [index]. */
    private var tries = 0

    private var hasFocus = false
    private var yielded = Yield.NONE
    private var speaking: String? = null // the utterance id being said
    private var watchdog: Cancellable? = null
    private var seq = 0L
    private var waitingFor: String? = null // the last reason logged for not speaking
    private var closed = false

    /** An answer waits for the phone to be unlocked. */
    var held = false
        private set

    /** Something is being said or waits to be. */
    val busy get() = current != null || queue.isNotEmpty()

    init {
        startEngine()
    }

    /** Reads text aloud after what is already queued. */
    fun say(text: String) {
        if (closed) return
        val spoken = SpeechText.forSpeech(text)
        if (spoken.isBlank()) return log("voice: the answer has nothing to read aloud")
        queue.addLast(spoken)
        if (yielded == Yield.FOR_GOOD) yielded = Yield.NONE
        if (engine == Engine.DOWN) startEngine()
        pump()
    }

    /** Silence, and the queue dropped. */
    fun stop() {
        if (closed) return
        queue.clear()
        endAnswer()
        silence()
        yielded = Yield.NONE
        pump()
    }

    /** On to the next answer. */
    fun skip() {
        if (closed) return
        endAnswer()
        silence()
        yielded = Yield.NONE
        pump()
    }

    /** Looks again whether it may speak, and goes on with an answer another app interrupted: after an unlock. */
    fun resume() {
        if (closed) return
        if (yielded != Yield.NONE) {
            // A short loss whose focus never came back holds nothing: ask anew.
            release()
            yielded = Yield.NONE
        }
        pump()
    }

    /** Looks again whether it may speak, without going on after an interruption: a call began or ended. */
    fun recheck() {
        if (!closed) pump()
    }

    fun shutdown() {
        if (closed) return
        silence()
        closed = true
        readyTimer?.cancel()
        restartTimer?.cancel()
        voice?.shutdown()
        voice = null
        engine = Engine.DOWN
    }

    /** The focus came back after a short loss. */
    fun focusGained() {
        if (closed) return
        if (yielded == Yield.BRIEFLY) yielded = Yield.NONE
        pump()
    }

    /** A call or a prompt took the focus for a moment: pause, and go on from this sentence when it is back. */
    fun focusLostBriefly() {
        if (closed || !hasFocus) return
        stopSpeaking()
        yielded = Yield.BRIEFLY
        waiting("another app has the audio focus for a moment; paused until it is back")
    }

    /** Another app took the focus for good: stop and let it go, until an unlock, a new answer or Skip. */
    fun focusLost() {
        if (closed || !hasFocus) return
        stopSpeaking()
        release()
        if (!busy) return changed()
        yielded = Yield.FOR_GOOD
        waiting("another app took the audio focus; the answer waits for an unlock, a new answer or Skip")
    }

    private fun startEngine() {
        readyTimer?.cancel()
        restartTimer?.cancel()
        restartTimer = null
        locale = null
        val v = newVoice()
        voice = v
        engine = Engine.STARTING
        v.onReady = { ok -> if (!closed && voice === v) ready(ok) }
        v.onDone = { id -> if (!closed && voice === v) done(id) }
        v.onError = { id, code -> if (!closed && voice === v) error(id, code) }
        readyTimer = later.after(READY_TIMEOUT_MS) {
            if (!closed && voice === v && engine == Engine.STARTING) {
                engineFailed("text-to-speech did not start within ${READY_TIMEOUT_MS / 1000} s")
            }
        }
    }

    private fun ready(ok: Boolean) {
        if (engine != Engine.STARTING) return
        readyTimer?.cancel()
        if (!ok) return engineFailed("text-to-speech is not available on this phone")
        engine = Engine.READY
        if (failures > 0) log("voice: text-to-speech started again")
        pump()
    }

    /** The engine could not start: a new one later, never in a tight loop. */
    private fun engineFailed(why: String) {
        failures++
        // Not even the first one at once: an engine that cannot bind says so right away.
        val wait = backoffMs(failures + 1)
        log("voice: $why; trying a new engine in ${wait / 1000} s")
        dropEngine()
        restartTimer = later.after(wait) { if (!closed && engine == Engine.DOWN) startEngine() }
        changed()
    }

    private fun dropEngine() {
        readyTimer?.cancel()
        restartTimer?.cancel()
        restartTimer = null
        stopSpeaking()
        release()
        voice?.shutdown()
        voice = null
        engine = Engine.DOWN
    }

    private fun done(id: String) {
        if (id != speaking) return
        watchdog?.cancel()
        speaking = null
        failures = 0
        tries = 0
        index++
        pump()
    }

    private fun error(id: String, code: Int) {
        if (id != speaking) return
        sentenceFailed("text-to-speech reported error $code")
    }

    /**
     * The sentence being said failed: let go of the focus, make a new engine and try the sentence
     * again on it, or drop the answer after [MAX_TRIES].
     */
    private fun sentenceFailed(why: String) {
        failures++
        tries++
        val drop = tries >= MAX_TRIES
        val wait = backoffMs(failures)
        val then = if (wait == 0L) "a new engine now" else "a new engine in ${wait / 1000} s"
        if (drop) {
            log("voice: $why; dropped the answer after $tries tries, $then")
            endAnswer()
        } else {
            log("voice: $why; trying the sentence again with $then")
        }
        dropEngine()
        if (wait == 0L) {
            startEngine()
        } else {
            restartTimer = later.after(wait) { if (!closed && engine == Engine.DOWN) startEngine() }
        }
        changed()
    }

    private fun endAnswer() {
        current = null
        answer = null
        index = 0
        tries = 0
        locale = null
    }

    /** Stops what is being said; the sentence stays where it is. */
    private fun stopSpeaking() {
        watchdog?.cancel()
        watchdog = null
        if (speaking != null) {
            speaking = null
            voice?.stop()
        }
    }

    private fun silence() {
        stopSpeaking()
        release()
    }

    private fun release() {
        if (hasFocus) focus.abandon()
        hasFocus = false
        // Without the focus no gain comes back: a short loss holds nothing any more.
        if (yielded == Yield.BRIEFLY) yielded = Yield.NONE
    }

    /** Logs why the answer is not said now, once per reason. */
    private fun waiting(why: String) {
        if (why != waitingFor) log("voice: not speaking now: $why")
        waitingFor = why
        changed()
    }

    private fun pump() {
        if (closed || speaking != null) return
        var pieces = current
        while (pieces == null || index >= pieces.size) {
            if (pieces != null) {
                log("voice: answer spoken")
                endAnswer()
            }
            val next = queue.removeFirstOrNull() ?: return idle()
            answer = next
            pieces = SpeechText.sentences(next)
            current = pieces
            index = 0
            tries = 0
            locale = null
        }
        held = lockedQuiet()
        if (held) {
            release()
            return waiting("the phone is locked; the answer waits for an unlock")
        }
        when (yielded) {
            Yield.BRIEFLY -> return waiting("another app has the audio focus for a moment; paused until it is back")
            Yield.FOR_GOOD -> return waiting("another app took the audio focus; the answer waits for an unlock, a new answer or Skip")
            Yield.NONE -> {}
        }
        if (inCall()) {
            release()
            return waiting("a call is on")
        }
        val v = voice
        if (engine != Engine.READY || v == null) {
            release()
            return waiting(if (engine == Engine.STARTING) "text-to-speech is starting" else "text-to-speech failed; waiting for a new engine")
        }
        val fresh = locale == null
        val loc = locale ?: localeFor(v, answer.orEmpty()).also { locale = it }
        if (!hasFocus) {
            if (!focus.request()) return waiting("the audio focus was denied")
            hasFocus = true
        }
        if (fresh || waitingFor != null) {
            log("voice: speaking an answer from sentence ${index + 1} of ${pieces.size}, voice ${loc.toLanguageTag()}")
        }
        waitingFor = null
        val piece = pieces[index]
        val id = "a${++seq}"
        speaking = id
        val limit = timeoutMs(piece)
        watchdog = later.after(limit) {
            if (!closed && speaking == id) sentenceFailed("text-to-speech said nothing back within ${limit / 1000} s")
        }
        if (!v.speak(piece, loc, id)) {
            if (speaking == id) sentenceFailed("text-to-speech did not take the sentence")
            return
        }
        changed()
    }

    /** Nothing waits: no focus, nothing held. */
    private fun idle() {
        held = false
        yielded = Yield.NONE
        waitingFor = null
        release()
        changed()
    }

    /** German or English as the answer reads, where the phone has that voice; else the phone's language. */
    private fun localeFor(v: SpeechVoice, text: String): Locale {
        val want = when (SpeechText.language(text)) {
            "de" -> Locale.GERMANY
            "en" -> Locale.US
            else -> return Locale.getDefault()
        }
        if (v.supports(want)) return want
        log("voice: the phone has no ${want.toLanguageTag()} voice; using its default voice")
        return Locale.getDefault()
    }
}

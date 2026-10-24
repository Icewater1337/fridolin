package local.plaudsync

import android.app.KeyguardManager
import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.os.Handler
import android.os.Looper
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import java.util.Locale

/** Voice mode's switches (docs/PLAUD.md, "Voice mode"), kept on the phone. */
object VoicePrefs {
    private fun prefs(ctx: Context) = ctx.getSharedPreferences("voice", Context.MODE_PRIVATE)

    /** Voice mode: NotePin recordings are questions, and answers are read aloud. */
    fun enabled(ctx: Context) = prefs(ctx).getBoolean("enabled", false)
    fun setEnabled(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("enabled", on).apply()

    /** Answers may be spoken while the phone is locked; otherwise they wait for it to be unlocked. */
    fun speakLocked(ctx: Context) = prefs(ctx).getBoolean("speak_locked", false)
    fun setSpeakLocked(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("speak_locked", on).apply()
}

/**
 * A voice that speaks one piece of text at a time. [AndroidVoice], the phone's own on-device
 * TextToSpeech, is the one there is; a better one (a server-side voice, say) can take its place
 * behind this interface without [Speaker] changing.
 */
interface SpeechVoice {
    /** Called once: whether the voice can speak at all. */
    var onReady: (Boolean) -> Unit
    /** Called when the piece with this id was said to its end (or could not be). */
    var onDone: (String) -> Unit
    fun speak(text: String, locale: Locale, id: String)
    /** Stops the piece being said; its onDone is not called. */
    fun stop()
    fun supports(locale: Locale): Boolean
    fun shutdown()
}

/** Android's TextToSpeech, on the device: free, offline where the phone has the voice. */
class AndroidVoice(ctx: Context, attrs: AudioAttributes) : SpeechVoice {
    override var onReady: (Boolean) -> Unit = {}
    override var onDone: (String) -> Unit = {}
    private val tts: TextToSpeech = TextToSpeech(ctx) { status -> onReady(status == TextToSpeech.SUCCESS) }

    init {
        tts.setAudioAttributes(attrs)
        val said = { id: String -> onDone(id) }
        tts.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
            override fun onStart(id: String) {}
            override fun onDone(id: String) = said(id)

            @Deprecated("Deprecated in Java")
            override fun onError(id: String) = said(id)
            override fun onStop(id: String, interrupted: Boolean) {} // stopped on purpose: stop, skip or a call
        })
    }

    override fun speak(text: String, locale: Locale, id: String) {
        tts.language = locale
        tts.speak(text, TextToSpeech.QUEUE_FLUSH, null, id)
    }

    override fun stop() {
        tts.stop()
    }

    override fun supports(locale: Locale) = tts.isLanguageAvailable(locale) >= TextToSpeech.LANG_AVAILABLE
    override fun shutdown() = tts.shutdown()
}

/**
 * Reads Fridolin's answers aloud with a [SpeechVoice] (the phone's TextToSpeech), one after the other. It takes
 * transient audio focus with usage "assistant", so music ducks meanwhile; when it loses focus (a
 * call rings) it stops and goes on from the sentence it was in once it has focus back. Nothing
 * starts during a call, nor while the phone is locked unless [VoicePrefs.speakLocked]: an answer
 * then waits ([held]) until the service sees the phone unlocked and calls [resume].
 *
 * Use it on the main thread only.
 */
class Speaker(private val ctx: Context, private val changed: () -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    private val audio = ctx.getSystemService(AudioManager::class.java)
    private val keyguard = ctx.getSystemService(KeyguardManager::class.java)
    private val attrs = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_ASSISTANT)
        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
        .build()
    private val focus = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
        .setAudioAttributes(attrs)
        .setOnAudioFocusChangeListener({ onFocus(it) }, main)
        .build()

    private var ready = false
    private val queue = ArrayDeque<String>()
    private var current: List<String>? = null
    private var index = 0
    private var locale: Locale = Locale.getDefault()
    private var hasFocus = false
    private var lostFocus = false
    private var speaking: String? = null // the utterance id being spoken

    /** An answer waits for the phone to be unlocked. */
    var held = false
        private set

    /** Something is being said or waits to be. */
    val busy get() = current != null || queue.isNotEmpty()

    private val tts: SpeechVoice = AndroidVoice(ctx, attrs).apply {
        onReady = { ok ->
            main.post {
                ready = ok
                if (!ok) AppLog.i("voice: text-to-speech is not available on this phone") else pump()
            }
        }
        onDone = { id -> main.post { finished(id) } }
    }

    /** Utterance id was said: on to the next piece. */
    private fun finished(id: String) {
        if (id != speaking) return
        speaking = null
        index++
        pump()
    }

    private val modeListener = AudioManager.OnModeChangedListener { main.post { pump() } }

    init {
        audio.addOnModeChangedListener(ctx.mainExecutor, modeListener)
    }

    /** Reads text aloud after what is already queued. */
    fun say(text: String) {
        val spoken = SpeechText.forSpeech(text)
        if (spoken.isBlank()) return
        queue.addLast(spoken)
        pump()
    }

    /** Silence, and the queue dropped. */
    fun stop() {
        queue.clear()
        current = null
        silence()
        changed()
    }

    /** On to the next answer. */
    fun skip() {
        current = null
        silence()
        pump()
    }

    /** Looks again whether it may speak: after an unlock, say. */
    fun resume() = pump()

    fun shutdown() {
        audio.removeOnModeChangedListener(modeListener)
        silence()
        tts.shutdown()
    }

    private fun silence() {
        speaking = null
        tts.stop()
        if (hasFocus) audio.abandonAudioFocusRequest(focus)
        hasFocus = false
    }

    private fun inCall() = audio.mode == AudioManager.MODE_IN_CALL || audio.mode == AudioManager.MODE_IN_COMMUNICATION || audio.mode == AudioManager.MODE_RINGTONE

    private fun pump() {
        if (!ready || speaking != null) return
        if (current == null) {
            val next = queue.removeFirstOrNull()
            if (next == null) {
                held = false
                if (hasFocus) audio.abandonAudioFocusRequest(focus)
                hasFocus = false
                return changed()
            }
            current = SpeechText.sentences(next)
            index = 0
            locale = localeFor(next)
        }
        val pieces = current!!
        if (index >= pieces.size) {
            current = null
            return pump()
        }
        val locked = keyguard.isKeyguardLocked && !VoicePrefs.speakLocked(ctx)
        held = locked
        if (locked || inCall() || lostFocus) return changed()
        if (!hasFocus) {
            if (audio.requestAudioFocus(focus) != AudioManager.AUDIOFOCUS_REQUEST_GRANTED) return changed()
            hasFocus = true
        }
        val id = "a${System.nanoTime()}"
        speaking = id
        tts.speak(pieces[index], locale, id)
        changed()
    }

    private fun onFocus(change: Int) {
        when (change) {
            AudioManager.AUDIOFOCUS_GAIN -> {
                lostFocus = false
                pump()
            }
            AudioManager.AUDIOFOCUS_LOSS, AudioManager.AUDIOFOCUS_LOSS_TRANSIENT -> {
                // A call, or another app that speaks: stop, and go on from this sentence later.
                lostFocus = change == AudioManager.AUDIOFOCUS_LOSS_TRANSIENT
                speaking = null
                tts.stop()
                if (change == AudioManager.AUDIOFOCUS_LOSS) {
                    hasFocus = false
                    main.postDelayed({ pump() }, 2_000)
                }
                changed()
            }
        }
    }

    /** German or English as the answer reads, where the phone has that voice; else the phone's language. */
    private fun localeFor(text: String): Locale {
        val want = when (SpeechText.language(text)) {
            "de" -> Locale.GERMANY
            "en" -> Locale.US
            else -> return Locale.getDefault()
        }
        return if (tts.supports(want)) want else Locale.getDefault()
    }
}

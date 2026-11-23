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

    /** Voice mode: NotePin recordings go to the voice brain, which answers aloud, as it does questions. */
    fun enabled(ctx: Context) = prefs(ctx).getBoolean("enabled", false)
    fun setEnabled(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("enabled", on).apply()

    /** Answers may be spoken while the phone is locked; otherwise they wait for it to be unlocked. */
    fun speakLocked(ctx: Context) = prefs(ctx).getBoolean("speak_locked", false)
    fun setSpeakLocked(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("speak_locked", on).apply()
}

/**
 * A voice that speaks one piece of text at a time. [AndroidVoice], the phone's own on-device
 * TextToSpeech, is the one there is; a better one (a server-side voice, say) can take its place
 * behind this interface without [SpeakerCore] changing. Its callbacks come on the speaker's thread.
 */
interface SpeechVoice {
    /** Called once: whether the voice can speak at all. */
    var onReady: (Boolean) -> Unit
    /** Called when the piece with this id was said to its end. */
    var onDone: (String) -> Unit
    /** Called when the piece with this id could not be said, with the engine's error code. */
    var onError: (String, Int) -> Unit
    /** Starts saying the piece; false when the voice did not take it, and then no callback comes for it. */
    fun speak(text: String, locale: Locale, id: String): Boolean
    /** Stops the piece being said; neither onDone nor onError is called for it. */
    fun stop()
    fun supports(locale: Locale): Boolean
    fun shutdown()
}

/**
 * Android's TextToSpeech, on the device: free, offline where the phone has the voice. Its
 * callbacks come on [main], so that one made in a constructor (an engine that cannot be bound
 * reports that at once) reaches the callback set after it.
 */
class AndroidVoice(ctx: Context, attrs: AudioAttributes, private val main: Handler) : SpeechVoice {
    override var onReady: (Boolean) -> Unit = {}
    override var onDone: (String) -> Unit = {}
    override var onError: (String, Int) -> Unit = { _, _ -> }
    private val tts: TextToSpeech = TextToSpeech(ctx) { status -> main.post { onReady(status == TextToSpeech.SUCCESS) } }

    init {
        tts.setAudioAttributes(attrs)
        tts.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
            override fun onStart(id: String) {}
            override fun onDone(id: String) = said(id)
            override fun onError(id: String, errorCode: Int) = failed(id, errorCode)

            @Deprecated("Deprecated in Java")
            override fun onError(id: String) = failed(id, TextToSpeech.ERROR)
            override fun onStop(id: String, interrupted: Boolean) {} // stopped on purpose: stop, skip or a call
        })
    }

    private fun said(id: String) {
        main.post { onDone(id) }
    }

    private fun failed(id: String, code: Int) {
        main.post { onError(id, code) }
    }

    override fun speak(text: String, locale: Locale, id: String): Boolean {
        tts.language = locale
        // ERROR when the engine is not bound (its service died, or was updated, under a
        // long-running app): then no callback ever comes.
        return tts.speak(text, TextToSpeech.QUEUE_FLUSH, null, id) == TextToSpeech.SUCCESS
    }

    override fun stop() {
        tts.stop()
    }

    override fun supports(locale: Locale) = tts.isLanguageAvailable(locale) >= TextToSpeech.LANG_AVAILABLE
    override fun shutdown() = tts.shutdown()
}

/**
 * Reads Fridolin's answers aloud with the phone's TextToSpeech, one after the other: the Android
 * side of [SpeakerCore], which decides. It asks for transient audio focus with usage "assistant"
 * only while it speaks, so music ducks meanwhile and comes back as soon as it is done. A call
 * pauses it, and it goes on from the sentence it was in once the call is over. Another app that
 * takes the focus for good (a video) stops it until the phone is unlocked again, a new answer
 * comes or Skip. Nothing starts during a call, nor while the phone is locked unless
 * [VoicePrefs.speakLocked]: an answer then waits ([held]) until the service sees the phone
 * unlocked and calls [resume].
 *
 * Use it on the main thread only.
 */
class Speaker(private val ctx: Context, changed: () -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    private val audio = ctx.getSystemService(AudioManager::class.java)
    private val keyguard = ctx.getSystemService(KeyguardManager::class.java)
    private val attrs = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_ASSISTANT)
        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
        .build()
    private val request = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
        .setAudioAttributes(attrs)
        .setOnAudioFocusChangeListener({ onFocus(it) }, main)
        .build()

    private val core = SpeakerCore(
        newVoice = { AndroidVoice(ctx, attrs, main) },
        focus = object : FocusPort {
            override fun request() = audio.requestAudioFocus(request) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
            override fun abandon() {
                audio.abandonAudioFocusRequest(request)
            }
        },
        later = { delayMs, task ->
            val r = Runnable(task)
            main.postDelayed(r, delayMs)
            Cancellable { main.removeCallbacks(r) }
        },
        lockedQuiet = { keyguard.isKeyguardLocked && !VoicePrefs.speakLocked(ctx) },
        inCall = {
            audio.mode == AudioManager.MODE_IN_CALL || audio.mode == AudioManager.MODE_IN_COMMUNICATION ||
                audio.mode == AudioManager.MODE_RINGTONE
        },
        log = AppLog::i,
        changed = changed,
    )

    /** An answer waits for the phone to be unlocked. */
    val held get() = core.held

    /** Something is being said or waits to be. */
    val busy get() = core.busy

    private val modeListener = AudioManager.OnModeChangedListener { main.post { core.recheck() } }

    init {
        audio.addOnModeChangedListener(ctx.mainExecutor, modeListener)
    }

    /** Reads text aloud after what is already queued. */
    fun say(text: String) = core.say(text)

    /** Silence, and the queue dropped. */
    fun stop() = core.stop()

    /** On to the next answer. */
    fun skip() = core.skip()

    /** Looks again whether it may speak, and goes on with an interrupted answer: after an unlock. */
    fun resume() = core.resume()

    fun shutdown() {
        audio.removeOnModeChangedListener(modeListener)
        core.shutdown()
    }

    private fun onFocus(change: Int) {
        when (change) {
            AudioManager.AUDIOFOCUS_GAIN -> core.focusGained()
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT -> core.focusLostBriefly() // a call rings, a prompt speaks
            AudioManager.AUDIOFOCUS_LOSS -> core.focusLost() // a video or music took it
        }
    }
}

package local.plaudsync

/**
 * What waits in voice/ to be sent to Fridolin in voice mode (docs/PLAUD.md, "Voice mode"), and what
 * to do when Fridolin does not take it. Pure functions, tested on the JVM (VoiceQueueTest).
 *
 * A question asked with Ask is `<id>.ask`, its text. A recording from the pin is `<id>.pin`: the
 * recording's name (the time it started, as its MP3 and transcript are called) on the first line,
 * then Plaud's transcript, whole, speaker labels and timestamps too. Once sent, either is
 * `<id>.wait` until its answer is there.
 */
object VoiceQueue {
    const val ASK = "ask"
    const val PIN = "pin"

    private val name = Regex("""\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}""")

    /** A recording from the pin as it waits in its `.pin` file. */
    data class Pin(val name: String, val transcript: String)

    /** What a `.pin` file holds for the recording [name] with this transcript. */
    fun pin(name: String, transcript: String): String {
        require(this.name.matches(name)) { "not a recording's name: $name" }
        return "$name\n$transcript"
    }

    /** The recording a `.pin` file holds; null when it holds none. */
    fun readPin(content: String): Pin? {
        val nl = content.indexOf('\n')
        if (nl < 0 || !name.matches(content.substring(0, nl))) return null
        return Pin(content.substring(0, nl), content.substring(nl + 1))
    }

    /**
     * Whether Fridolin's answer with this HTTP status refuses the request for good, so that sending
     * the same again would be refused again (too long, voice mode off in Fridolin, no brain for
     * it): any 4xx but 401 (unpaired: the next try pairs again), 408 and 429 (later). A 5xx, or no
     * answer at all, is worth another try. A refused recording from the pin goes as a transcript
     * instead, and a refused question is dropped, so that neither holds up what comes after it.
     */
    fun refused(code: Int) = code in 400..499 && code != 401 && code != 408 && code != 429
}

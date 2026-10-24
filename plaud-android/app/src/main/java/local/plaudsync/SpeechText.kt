package local.plaudsync

/**
 * Text for the ear (voice mode, docs/PLAUD.md): Fridolin's answers are written for WhatsApp, so
 * markdown, links, polls and emoji are turned into something text-to-speech can read. Pure
 * functions, tested on the JVM (SpeechTextTest).
 */
object SpeechText {
    private val poll = Regex("""(?s)\[POLL[^\]]*]\s*(.*?)\[/POLL]""")
    private val fence = Regex("""(?m)^\s*```.*$""")
    private val mdLink = Regex("""\[([^\]]+)]\((https?://[^)\s]+)\)""")
    private val url = Regex("""https?://(?:www\.)?([A-Za-z0-9.-]+)[^\s)]*""")
    private val heading = Regex("""(?m)^\s{0,3}#{1,6}\s+""")
    private val bullet = Regex("""(?m)^\s*[-*+•]\s+""")
    private val quote = Regex("""(?m)^\s*>\s?""")
    private val emphasis = Regex("""(\*\*|__|\*|_|~~|`)(\S(?:.*?\S)?)\1""")
    private val tableRule = Regex("""(?m)^\s*\|?\s*:?-{3,}.*$""")
    private val emoji = Regex("""[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}\x{200D}\x{20E3}]""")
    private val spaces = Regex("""[ \t]+""")

    /** Fridolin's answer, as it should be spoken. */
    fun forSpeech(text: String): String {
        var s = poll.replace(text) { m ->
            val lines = m.groupValues[1].lines().map { it.trim() }.filter { it.isNotEmpty() }
            val q = lines.firstOrNull { it.startsWith("Question:") }?.removePrefix("Question:")?.trim() ?: ""
            val options = lines.filter { it.startsWith("- ") }.map { it.removePrefix("- ").trim() }
            "Poll: ${end(q)} Options: ${options.joinToString(", ")}.\n"
        }
        s = fence.replace(s, "")
        s = mdLink.replace(s) { it.groupValues[1] }
        s = url.replace(s) { m ->
            // A sentence's full stop after a link is not part of it.
            val tail = m.value.takeLastWhile { it in ".,;:!?" }
            "a link to ${m.groupValues[1].trimEnd('.')}$tail"
        }
        s = tableRule.replace(s, "")
        s = heading.replace(s, "")
        s = bullet.replace(s, "")
        s = quote.replace(s, "")
        repeat(2) { s = emphasis.replace(s) { it.groupValues[2] } }
        s = s.lines().joinToString("\n") { line ->
            if (line.count { it == '|' } >= 2) line.trim().trim('|').split('|').joinToString(", ") { it.trim() } else line
        }
        s = emoji.replace(s, "")
        return s.lines().map { spaces.replace(it, " ").trim() }.filter { it.isNotEmpty() }
            .joinToString("\n")
    }

    private fun end(s: String) = if (s.isEmpty() || s.last() in ".?!:") s else "$s."

    /**
     * Pieces of what to say, each a sentence or a line, none longer than max: speech stopped by a
     * call goes on from the piece it was in.
     */
    fun sentences(text: String, max: Int = 3000): List<String> =
        text.split(Regex("""(?<=[.!?…])\s+|\n+""")).map { it.trim() }.filter { it.isNotEmpty() }
            .flatMap { it.chunked(max) }

    private val german = setOf(
        "der", "die", "das", "und", "ist", "nicht", "ich", "du", "sie", "es", "wir", "ein", "eine", "einen",
        "mit", "auf", "für", "von", "zu", "den", "dem", "im", "auch", "noch", "aber", "wie", "was", "wann",
        "heute", "morgen", "um", "uhr", "hast", "habe", "bin", "sind", "wird", "kein", "keine", "dass", "oder",
    )
    private val english = setOf(
        "the", "and", "is", "not", "i", "you", "it", "we", "a", "an", "with", "on", "for", "of", "to", "in",
        "also", "but", "how", "what", "when", "today", "tomorrow", "at", "have", "am", "are", "will", "no",
        "that", "or", "your", "this", "there", "be", "do", "does",
    )

    /** "de" or "en", whichever the text reads as more; null when it can't tell. */
    fun language(text: String): String? {
        val words = text.lowercase().split(Regex("""[^\p{L}]+""")).filter { it.isNotEmpty() }
        val de = words.count { it in german } + 2 * text.count { it in "äöüßÄÖÜ" }
        val en = words.count { it in english }
        return if (de == en) null else if (de > en) "de" else "en"
    }

    private val label = Regex("""^\s*\[\d{1,2}:\d{2}(?::\d{2})?\s*-\s*\d{1,2}:\d{2}(?::\d{2})?]\s*(?:[^:\n]{1,40}:\s*)?""")

    /** A Plaud transcript ("[00:00 - 00:04] Speaker 1: …") as a question: the words alone. */
    fun question(transcript: String): String =
        transcript.lines().map { label.replace(it, "").trim() }.filter { it.isNotEmpty() }.joinToString(" ")
}

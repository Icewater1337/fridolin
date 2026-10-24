package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SpeechTextTest {
    @Test
    fun markdownIsStripped() {
        val s = SpeechText.forSpeech("## Tomorrow\n- **9:00** dentist\n- *10:30* call with `Anna`\n> note\n```\ncode\n```")
        assertEquals("Tomorrow\n9:00 dentist\n10:30 call with Anna\nnote\ncode", s)
    }

    @Test
    fun linksAreShortenedToTheirSite() {
        assertEquals("See the docs and a link to example.com.",
            SpeechText.forSpeech("See [the docs](https://example.com/a/b?c=d) and https://www.example.com/x/y."))
    }

    @Test
    fun pollsAreReadAsAList() {
        val s = SpeechText.forSpeech("Pick one:\n[POLL max=2]\nQuestion: When should we meet\n- Monday 2pm\n- Tuesday\n[/POLL]")
        assertEquals("Pick one:\nPoll: When should we meet. Options: Monday 2pm, Tuesday.", s)
    }

    @Test
    fun emojiAndTablesGo() {
        assertEquals("Done\nDay, Time\nMon, 9", SpeechText.forSpeech("Done ✅🎉\n| Day | Time |\n|---|---|\n| Mon | 9 |"))
    }

    @Test
    fun sentencesSplitWhereSpeechMayStopAndGoOn() {
        assertEquals(listOf("Hallo.", "Wie geht's?", "Gut!", "Neue Zeile"), SpeechText.sentences("Hallo. Wie geht's? Gut!\nNeue Zeile"))
        assertTrue(SpeechText.sentences("x".repeat(7000)).all { it.length <= 3000 })
    }

    @Test
    fun languageIsGermanOrEnglish() {
        assertEquals("de", SpeechText.language("Morgen um neun hast du einen Termin beim Zahnarzt."))
        assertEquals("en", SpeechText.language("Tomorrow at nine you have a dentist appointment."))
        assertEquals("de", SpeechText.language("Grüße"))
        assertNull(SpeechText.language("9:00"))
    }

    @Test
    fun aTranscriptBecomesItsWords() {
        assertEquals("Was steht morgen an? Und übermorgen?",
            SpeechText.question("[00:00 - 00:04] Speaker 1: Was steht morgen an?\n[00:04 - 00:06] Speaker 1: Und übermorgen?\n"))
        assertEquals("plain", SpeechText.question("plain"))
    }
}

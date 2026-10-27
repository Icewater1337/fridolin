package local.plaudsync

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Test

class RunAgainTest {
    // 2026-10-27: three recordings came off the pin in one check-in. The first one's
    // transcription had started when the other two were saved, and their calls were dropped:
    // only the first was transcribed, and the others waited for the next recording.
    @Test
    fun recordingsSavedWhileOneIsTranscribedAreTranscribedToo() = runBlocking {
        val pending = mutableListOf("06-07-10")
        val transcribed = mutableListOf<String>()
        val busy = CompletableDeferred<Unit>()
        val queue = RunAgain()
        // A pass takes what is in the folder when it starts, as the app's pass did then
        val pass: suspend () -> Unit = {
            val batch = pending.toList()
            pending.clear()
            for (next in batch) {
                if (transcribed.isEmpty()) busy.await() // Plaud takes a while
                transcribed += next
            }
        }
        val first = launch { queue.run(pass) }
        yield() // the first pass has taken 06-07-10 and waits for Plaud
        pending += "09-27-29"
        queue.run(pass) // saved meanwhile: returns at once
        pending += "09-28-18"
        queue.run(pass)
        busy.complete(Unit)
        first.join()
        assertEquals(listOf("06-07-10", "09-27-29", "09-28-18"), transcribed)
    }

    @Test
    fun callsWhileAPassRunsMakeOneMorePass() = runBlocking {
        var passes = 0
        val busy = CompletableDeferred<Unit>()
        val queue = RunAgain()
        val pass: suspend () -> Unit = {
            passes++
            if (passes == 1) busy.await()
        }
        val first = launch { queue.run(pass) }
        yield()
        repeat(3) { queue.run(pass) }
        busy.complete(Unit)
        first.join()
        assertEquals(2, passes)
        queue.run(pass)
        assertEquals(3, passes)
    }

    @Test
    fun aPassThatThrowsDoesNotStopTheNextCall() = runBlocking {
        val queue = RunAgain()
        runCatching { queue.run { error("Plaud is down") } }
        var ran = false
        queue.run { ran = true }
        assertEquals(true, ran)
    }
}

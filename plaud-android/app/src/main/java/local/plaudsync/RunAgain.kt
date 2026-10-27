package local.plaudsync

/**
 * Runs a job one pass at a time without dropping a call: a call that comes while a pass runs
 * makes one more pass once it ends. Every call names the same job.
 *
 * A Mutex's tryLock dropped such a call. Several recordings come off the pin in one check-in,
 * each asking for transcription the moment it is saved; the first one's pass had listed the
 * folder already, so the others waited for the next recording, hours or days later.
 *
 * Call [run] from one thread (the service's main thread). Pure, so it is tested on the JVM.
 */
class RunAgain {
    private var running = false
    private var again = false

    /** Runs [pass], and once more for the calls that came meanwhile; returns at once while a pass runs. */
    suspend fun run(pass: suspend () -> Unit) {
        if (running) {
            again = true
            return
        }
        running = true
        try {
            do {
                again = false
                pass()
            } while (again)
        } finally {
            running = false
        }
    }
}

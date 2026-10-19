package local.plaudsync

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Restart the sync service after a reboot or an app update. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        SyncService.start(ctx)
    }
}

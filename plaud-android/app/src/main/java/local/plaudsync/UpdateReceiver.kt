package local.plaudsync

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller

/** How Android's install of an update went ([Updater.install]). */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        AppLog.init(ctx)
        val code = intent.getLongExtra(Updater.EXTRA_CODE, 0)
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val ask = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT) ?: return Updater.failed(ctx, code, "Android asked for a confirmation it gave no screen for")
                Updater.confirm(ctx, code, ask)
            }
            PackageInstaller.STATUS_SUCCESS -> Updater.installed(ctx, code)
            PackageInstaller.STATUS_FAILURE_ABORTED -> Updater.failed(ctx, code, "it was not confirmed on the phone; opening the app asks again")
            else -> Updater.failed(ctx, code, intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "Android's status $status")
        }
    }
}

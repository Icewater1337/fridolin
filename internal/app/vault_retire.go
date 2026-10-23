package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Retiring a single-layer folder of before (fridolin vault
// migrate-encryption --delete-old) has to cope with what the stores say
// when there is nothing, or nothing reachable, left to delete:
//
//   - a folder that is not there is retired already: an earlier run, or
//     the owner, deleted it;
//   - on Google Drive the remote's scope is drive.file, which reaches only
//     what its own OAuth client made. A folder that holds something
//     another client made cannot be deleted (403 appNotAuthorizedToChild),
//     and once the owner's own client replaces rclone's shared one, the
//     folders the shared one made are not even listed ("directory not
//     found"). Neither leaves anything fridolin can reach, so the copy
//     counts as retired, and what may stay in Drive is reported once,
//     with the folder's name, for the owner to delete there by hand.

// oldPurger empties one single-layer folder of before. left, when not
// empty, says what may stay that only the owner can delete.
type oldPurger func(ctx context.Context) (left string, err error)

// driveLeftNote is what may stay in Google Drive after the single-layer
// repository is retired.
func driveLeftNote(folder string, refused bool) string {
	if refused {
		return fmt.Sprintf("Google Drive did not let fridolin delete the folder %q (it holds items another Google client made, which the drive.file scope cannot reach); what fridolin could reach is deleted. Delete the folder %q in Google Drive by hand (My Drive, then empty the bin).", folder, folder)
	}
	return fmt.Sprintf("the folder %q is gone for fridolin's Google client; if Google Drive still shows it (made under an earlier Google client, which the drive.file scope cannot see), delete %q there by hand (My Drive, then empty the bin).", folder, folder)
}

// rcloneRun runs rclone op on target and returns its standard output, or
// an error with what it said, the secrets masked.
type rcloneRun func(ctx context.Context, args ...string) (string, error)

func execRclone(rclone string, env, secrets []string) rcloneRun {
	return func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, rclone, args...)
		cmd.Env = env
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
			for _, sec := range secrets {
				if sec != "" {
					msg = strings.ReplaceAll(msg, sec, "…")
				}
			}
			return "", fmt.Errorf("rclone %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return stdout.String(), nil
	}
}

// rcloneDirNotFound reports whether err is rclone's "directory not found".
func rcloneDirNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "directory not found")
}

// rcloneNotAuthorized reports whether err is Drive refusing an item the
// drive.file scope does not reach.
func rcloneNotAuthorized(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "appNotAuthorizedToChild") || strings.Contains(s, "appNotAuthorizedToFile") ||
		strings.Contains(s, "insufficientFilePermissions")
}

// rcloneRetire empties target with rclone purge. A target that is not
// there is retired already. With drive set (target is the folder name on
// a Drive remote), a folder Drive does not let fridolin delete is emptied
// of what fridolin can reach (rclone delete, then rmdirs), and when no
// file it can list is left the copy counts as retired, with a note for
// the owner; a folder fridolin's client cannot see counts as retired with
// a note too.
func rcloneRetire(ctx context.Context, run rcloneRun, target, driveFolder string) (string, error) {
	_, err := run(ctx, "purge", target)
	switch {
	case err == nil:
		return "", nil
	case rcloneDirNotFound(err):
		if driveFolder != "" {
			return driveLeftNote(driveFolder, false), nil
		}
		return "", nil
	case driveFolder == "" || !rcloneNotAuthorized(err):
		return "", err
	}
	// Drive refused the folder as a whole: delete what can be reached.
	_, delErr := run(ctx, "delete", target)
	_, rmErr := run(ctx, "rmdirs", target)
	files, lsErr := run(ctx, "lsf", "-R", "--files-only", target)
	switch {
	case rcloneDirNotFound(lsErr):
		return "", nil
	case lsErr != nil:
		return "", fmt.Errorf("%w (then listing what is left: %w)", err, lsErr)
	case len(strings.Fields(files)) > 0:
		return "", fmt.Errorf("%d files are left in %s: %w", len(strings.Fields(files)), target, errors.Join(err, delErr, rmErr))
	}
	return driveLeftNote(driveFolder, true), nil
}

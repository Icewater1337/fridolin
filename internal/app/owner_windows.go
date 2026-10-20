package app

import "os"

// ownedByOther and openToOthers say no on Windows: who owns a folder and
// who may read it is in its ACL, which the bridge does not read, and the
// mode Go reports there is 0777 for every folder. So the bridge neither
// warns about the data dir nor about db_path's folder there; a data dir
// under the user's profile is private to them by its inherited ACL.
func ownedByOther(string) bool { return false }

func openToOthers(dir string) (os.FileMode, bool) {
	st, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	return st.Mode().Perm(), false
}

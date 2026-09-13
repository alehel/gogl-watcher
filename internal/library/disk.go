package library

import "syscall"

// DiskUsage reports the free (available to this user) and total bytes of the
// filesystem holding dir, or zeros when it cannot be determined.
func DiskUsage(dir string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	bs := blockSize(&st)
	return int64(st.Bavail) * bs, int64(st.Blocks) * bs
}

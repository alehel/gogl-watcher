package library

import "syscall"

// blockSize is the unit of statfs' block counts. On Linux that is f_frsize;
// f_bsize is the preferred I/O size, which virtiofs (Docker Desktop and podman
// on a Mac share the library through it) reports as 1 MiB against 4 KiB
// fragments, making the disk look 256 times larger than it is.
func blockSize(st *syscall.Statfs_t) int64 {
	if st.Frsize > 0 {
		return int64(st.Frsize)
	}
	return int64(st.Bsize)
}

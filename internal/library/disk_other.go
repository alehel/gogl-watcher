//go:build !linux

package library

import "syscall"

// blockSize is the unit of statfs' block counts; on the BSDs f_bsize already
// is the fragment size.
func blockSize(st *syscall.Statfs_t) int64 { return int64(st.Bsize) }
